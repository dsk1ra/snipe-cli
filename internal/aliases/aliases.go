// Package aliases proposes other spellings of cv.md's skills from the postings,
// and writes the ones the user accepts into config/skills.yml.
//
// Embeddings propose and the user decides. On the probe in docs/REWORK-PLAN.md
// §4 no similarity threshold separates a real alias from a neighbouring tool,
// so nothing here applies itself.
package aliases

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase3"
)

// The cut that makes the queue worth reading. Over 228 reports' keywords it
// leaves 45 suggestions covering 307 term uses, Kafka → Apache Kafka (23
// postings) first. Lower and it fills with one-off phrasing; higher and
// Spring → Spring Boot (0.697) drops out.
const (
	MinSim      = 0.68
	MinPostings = 2
)

// instruct goes on the query side only, as the Qwen3 embedding docs advise.
// Without it no threshold separated aliases from neighbours at all.
const instruct = "Instruct: Given a technical skill, retrieve the same skill written differently\nQuery: "

type Target struct {
	Item string
	Sim  float64
}

// Suggestion is a term the postings use that names nothing in cv.md, with the
// cv.md items it embeds nearest to.
type Suggestion struct {
	Term     string   // as most postings spell it
	Targets  []Target // nearest first, at most three
	IDs      []int    // offers whose report keywords carry it
	Lower    int      // postings using it only in lowercase: an ordinary word?
	Rejected bool
}

// Suggest rebuilds the queue from every report's keywords and returns how many
// suggestions are pending. Earlier rejections are kept.
func Suggest(ctx context.Context, l paths.Layout, cfg *config.Config, ol *ollama.Client) (int, error) {
	var items []string
	seen := map[string]bool{}
	var forms []string
	for _, c := range phase3.ParseSkillCategories(cfg.CVRaw) {
		for _, it := range c.Items {
			if seen[it] {
				continue
			}
			seen[it] = true
			items = append(items, it)
			for _, f := range phase3.SkillForms(it, cfg.Aliases) {
				forms = append(forms, phase3.NormPhrase(f))
			}
		}
	}
	if len(items) == 0 {
		return 0, nil
	}

	rejected, err := rejections(l.SkillSuggestions())
	if err != nil {
		return 0, err
	}
	uses, err := keywords(l)
	if err != nil {
		return 0, err
	}
	var terms []string
	for k, u := range uses {
		if len(u.ids) < MinPostings || rejected[k] != nil || covered(k, forms) {
			continue
		}
		terms = append(terms, k)
	}
	sort.Strings(terms)

	var out []Suggestion
	if len(terms) > 0 {
		iv, err := embed.Batch(ctx, ol, items, ollama.ModelEmbed)
		if err != nil {
			return 0, err
		}
		q := make([]string, len(terms))
		for i, k := range terms {
			q[i] = instruct + uses[k].spelling()
		}
		tv, err := embed.Batch(ctx, ol, q, ollama.ModelEmbed)
		if err != nil {
			return 0, err
		}
		jds := readJDs(l)
		for i, k := range terms {
			t := nearest(tv[i], items, iv, 3)
			if len(t) == 0 || t[0].Sim < MinSim {
				continue
			}
			term := uses[k].spelling()
			out = append(out, Suggestion{Term: term, Targets: t, IDs: uses[k].ids, Lower: lowerOnly(term, jds)})
		}
	}
	// Read again: the TUI may have rejected something while the embedder ran,
	// and that decision must not be overwritten by this run's stale copy.
	rejected, err = rejections(l.SkillSuggestions())
	if err != nil {
		return 0, err
	}
	kept := out[:0]
	for _, s := range out {
		if rejected[strings.ToLower(s.Term)] == nil {
			kept = append(kept, s)
		}
	}
	pending := len(kept)
	for _, s := range rejected {
		kept = append(kept, *s)
	}
	return pending, Save(l.SkillSuggestions(), kept)
}

// covered is whether a posting using term already names some item, in which
// case an alias would change nothing.
func covered(term string, forms []string) bool {
	n := phase3.NormPhrase(term)
	for _, f := range forms {
		if strings.Contains(n, f) {
			return true
		}
	}
	return false
}

func nearest(q []float64, items []string, vecs [][]float64, k int) []Target {
	var out []Target
	for i, v := range vecs {
		s, err := embed.Cosine(q, v)
		if err != nil {
			continue
		}
		out = append(out, Target{items[i], s})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sim > out[j].Sim })
	if len(out) > k {
		out = out[:k]
	}
	return out
}

type use struct {
	ids   []int
	spell map[string]int
}

// spelling is the form most postings use, ties to the alphabetically first so
// the queue does not reshuffle between runs.
func (u use) spelling() string {
	best, n := "", -1
	for s, c := range u.spell {
		if c > n || c == n && s < best {
			best, n = s, c
		}
	}
	return best
}

var keywordsRe = regexp.MustCompile(`(?m)^## Keywords\n\n(.+)$`)

// keywords reads each evaluated offer's report for the terms Phase 2 extracted.
// The stage checkpoints holding them are deleted once an offer completes; the
// report's Keywords line is what lasts.
func keywords(l paths.Layout) (map[string]*use, error) {
	files, err := filepath.Glob(filepath.Join(l.Evals(), "*.json"))
	if err != nil {
		return nil, err
	}
	out := map[string]*use{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		// The id is the file's name, which is what everything else keys on;
		// inside, the evals carry it as a string.
		id, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".json"))
		var e struct {
			Filename string `json:"report_filename"`
			Path     string `json:"report_path"`
		}
		if err != nil || json.Unmarshal(b, &e) != nil {
			continue
		}
		// By name under this home first: report_path is absolute, and wrong
		// in any checkout but the one that wrote it.
		r, err := os.ReadFile(filepath.Join(l.Reports(), e.Filename))
		if err != nil && e.Path != "" {
			r, err = os.ReadFile(e.Path)
		}
		if err != nil {
			continue
		}
		m := keywordsRe.FindStringSubmatch(string(r))
		if m == nil {
			continue
		}
		done := map[string]bool{}
		for _, t := range strings.Split(m[1], ", ") {
			t = strings.TrimSpace(t)
			k := strings.ToLower(t)
			if t == "" || done[k] {
				continue
			}
			done[k] = true
			u := out[k]
			if u == nil {
				u = &use{spell: map[string]int{}}
				out[k] = u
			}
			u.ids = append(u.ids, id)
			u.spell[t]++
		}
	}
	for _, u := range out {
		sort.Ints(u.ids)
	}
	return out, nil
}

type jd struct{ raw, norm string }

func readJDs(l paths.Layout) []jd {
	files, _ := filepath.Glob(filepath.Join(l.JDs(), "*.txt"))
	out := make([]jd, 0, len(files))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil {
			out = append(out, jd{string(b), phase3.NormPhrase(string(b))})
		}
	}
	return out
}

// lowerOnly counts the postings that use term only in other casing. An alias
// matches case-insensitively, so "Spring" would also name Spring Boot in "start
// in spring" — the trap skills.example.yml warns about, counted.
func lowerOnly(term string, jds []jd) int {
	if strings.ToLower(term) == term {
		return 0
	}
	n := phase3.NormPhrase(term)
	c := 0
	for _, j := range jds {
		if strings.Contains(j.norm, n) && !strings.Contains(j.raw, term) {
			c++
		}
	}
	return c
}

// The queue file: one suggestion per line, most postings first.
//
//	term  status  ids  lower  item1  sim1  item2  sim2  item3  sim3
const header = "term\tstatus\tids\tlower\titem1\tsim1\titem2\tsim2\titem3\tsim3"

// Load reads the queue. A missing file is an empty queue.
func Load(path string) ([]Suggestion, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Suggestion
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		c := strings.Split(sc.Text(), "\t")
		if len(c) < 6 || c[0] == "term" {
			continue
		}
		s := Suggestion{Term: c[0], Rejected: c[1] == "rejected"}
		for _, id := range strings.Split(c[2], ",") {
			if n, err := strconv.Atoi(id); err == nil {
				s.IDs = append(s.IDs, n)
			}
		}
		s.Lower, _ = strconv.Atoi(c[3])
		for i := 4; i+1 < len(c); i += 2 {
			if sim, err := strconv.ParseFloat(c[i+1], 64); err == nil && c[i] != "" {
				s.Targets = append(s.Targets, Target{c[i], sim})
			}
		}
		if len(s.Targets) > 0 {
			out = append(out, s)
		}
	}
	return out, sc.Err()
}

// Save writes the queue, most postings first, via a rename so a reader never
// sees half a file.
func Save(path string, ss []Suggestion) error {
	sort.SliceStable(ss, func(i, j int) bool {
		if len(ss[i].IDs) != len(ss[j].IDs) {
			return len(ss[i].IDs) > len(ss[j].IDs)
		}
		return ss[i].Term < ss[j].Term
	})
	var b strings.Builder
	b.WriteString(header + "\n")
	for _, s := range ss {
		status := "pending"
		if s.Rejected {
			status = "rejected"
		}
		ids := make([]string, len(s.IDs))
		for i, id := range s.IDs {
			ids[i] = strconv.Itoa(id)
		}
		row := []string{s.Term, status, strings.Join(ids, ","), strconv.Itoa(s.Lower)}
		for _, t := range s.Targets {
			row = append(row, t.Item, strconv.FormatFloat(math.Round(t.Sim*1000)/1000, 'f', 3, 64))
		}
		b.WriteString(strings.Join(row, "\t") + "\n")
	}
	return writeAtomic(path, b.String())
}

func rejections(path string) (map[string]*Suggestion, error) {
	ss, err := Load(path)
	if err != nil {
		return nil, err
	}
	out := map[string]*Suggestion{}
	for i := range ss {
		if ss[i].Rejected {
			out[strings.ToLower(ss[i].Term)] = &ss[i]
		}
	}
	return out, nil
}

// Reject marks term as no alias of anything, so it is never suggested again.
func Reject(path, term string) error {
	return update(path, term, func(s *Suggestion) bool { s.Rejected = true; return true })
}

// Accept writes term into skills.yml under item and takes it off the queue.
func Accept(l paths.Layout, item, term string) error {
	if err := AddAlias(l.SkillsYML(), item, term); err != nil {
		return err
	}
	return update(l.SkillSuggestions(), term, func(*Suggestion) bool { return false })
}

// update applies fn to term's row; fn returning false drops the row.
func update(path, term string, fn func(*Suggestion) bool) error {
	ss, err := Load(path)
	if err != nil {
		return err
	}
	out := ss[:0]
	for i := range ss {
		if strings.EqualFold(ss[i].Term, term) && !fn(&ss[i]) {
			continue
		}
		out = append(out, ss[i])
	}
	return Save(path, out)
}

func writeAtomic(path, s string) error {
	// Through a symlink, not over it. The dev checkout links config/skills.yml
	// to the live install's, and a rename onto the link replaced it with a
	// plain file: the alias landed in dev only and the two copies forked.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(s); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
