package state

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Marks is an id → timestamp map, the shape shared by applied.tsv, skipped.tsv
// and p1-declined.tsv.
//
// These live on disk rather than in memory because the row list is rebuilt from
// local-state.tsv every second and from scratch on every launch — an in-memory
// "applied" or "declined" would come straight back.
type Marks map[int]time.Time

func LoadMarks(path string) Marks {
	m := Marks{}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(b), "\n") {
		id, ts, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(id))
		if err != nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(ts))
		if err != nil {
			continue
		}
		m[n] = t
	}
	return m
}

// Save writes the map back, id-ordered. The Node version preserved insertion
// order, which made the file churn on every rewrite; sorting makes it stable
// and diffable.
func (m Marks) Save(path string) error {
	ids := make([]int, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var b strings.Builder
	for _, id := range ids {
		// Node wrote Date#toISOString: always UTC, always milliseconds.
		fmt.Fprintf(&b, "%d\t%s\n", id, m[id].UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	return WriteAtomic(path, []byte(b.String()))
}

// Queue reads batch/snipe-queue.txt: ids waiting for the next drain.
func Queue(path string) []int {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ids []int
	for _, line := range strings.Split(string(b), "\n") {
		if n, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
			ids = append(ids, n)
		}
	}
	return ids
}

// Offer is a row of batch/batch-input.tsv: the pipeline's input list.
type Offer struct {
	ID     int
	URL    string
	Source string
	Notes  string
}

// Link reports the offer's job posting URL, if it has a real one. Placeholders
// (manual:snipe-N, local:…) are not links, and the UI renders those rows with
// no link rather than an unopenable one.
func (o Offer) Link() (string, bool) {
	if strings.HasPrefix(o.URL, "http://") || strings.HasPrefix(o.URL, "https://") {
		return o.URL, true
	}
	return "", false
}

func LoadOffers(path string) (map[int]Offer, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[int]Offer{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[int]Offer{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		id, err := strconv.Atoi(f[0])
		if err != nil {
			continue // the header row and any blank line
		}
		o := Offer{ID: id, URL: f[1]}
		if len(f) > 2 {
			o.Source = f[2]
		}
		if len(f) > 3 {
			o.Notes = f[3]
		}
		out[id] = o
	}
	return out, nil
}

// NextOfferID is max(id)+1, the id a newly pasted JD takes.
func NextOfferID(offers map[int]Offer) int {
	max := 0
	for id := range offers {
		if id > max {
			max = id
		}
	}
	return max + 1
}

// AppendOffer adds a row to batch-input.tsv, creating it with its header when
// absent. Append rather than rewrite: another process may hold the file, and
// the id was already reserved by the caller.
func AppendOffer(path string, o Offer) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() == 0 {
		if _, err := f.WriteString("id\turl\tsource\tnotes\n"); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(f, "%d\t%s\t%s\t%s\n", o.ID, o.URL, o.Source, o.Notes)
	return err
}
