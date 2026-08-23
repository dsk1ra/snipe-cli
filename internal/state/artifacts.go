package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Artifact is the shared shape of batch/scores/<id>.json and
// batch/evals/<id>.json. Both carry a status plus the identity fields the UI
// labels rows with; the eval adds a report number.
//
// A *failed* phase writes its fatal() payload to the same path — {error,
// status} with no report_num — so the file existing is not evidence the phase
// succeeded. Check Status, and for an eval, ReportNum.
type Artifact struct {
	Status    string  `json:"status"`
	Score     float64 `json:"score"`
	Company   string  `json:"company"`
	Role      string  `json:"role"`
	Archetype string  `json:"archetype"`
	ReportNum string  `json:"report_num"`
	Error     string  `json:"error"`
}

// Cache reads the pipeline's JSON artifacts, re-parsing a file only when its
// mtime moves. The TUI polls once a second over a few hundred files; without
// this that is a few hundred parses a second for data that changes minutely.
type Cache struct {
	mu    sync.Mutex
	files map[string]cached
	dirs  map[string]cachedDir
	act   *cachedActivity
}

type cached struct {
	mtime time.Time
	art   *Artifact // nil when the file is absent or unparseable
}

type cachedDir struct {
	mtime   time.Time
	entries []string
}

func NewCache() *Cache {
	return &Cache{files: map[string]cached{}, dirs: map[string]cachedDir{}}
}

func (c *Cache) read(path string) *Artifact {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if hit, ok := c.files[path]; ok && hit.mtime.Equal(st.ModTime()) {
		return hit.art
	}
	var a *Artifact
	if b, err := os.ReadFile(path); err == nil {
		var parsed Artifact
		if json.Unmarshal(b, &parsed) == nil {
			a = &parsed
		}
	}
	c.files[path] = cached{mtime: st.ModTime(), art: a}
	return a
}

// listDir caches a directory listing by the directory's own mtime, which moves
// whenever an entry is added or removed.
func (c *Cache) listDir(dir string) []string {
	st, err := os.Stat(dir)
	if err != nil {
		return nil
	}
	c.mu.Lock()
	if hit, ok := c.dirs[dir]; ok && hit.mtime.Equal(st.ModTime()) {
		c.mu.Unlock()
		return hit.entries
	}
	c.mu.Unlock()

	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	c.mu.Lock()
	c.dirs[dir] = cachedDir{mtime: st.ModTime(), entries: names}
	c.mu.Unlock()
	return names
}

func (c *Cache) Score(dir string, id int) *Artifact {
	return c.read(filepath.Join(dir, strconv.Itoa(id)+".json"))
}

func (c *Cache) Eval(dir string, id int) *Artifact {
	return c.read(filepath.Join(dir, strconv.Itoa(id)+".json"))
}

// Label names a row: "Company — Role", preferring the eval's identification
// over the pre-screen's, falling back to "#id" when neither has landed.
func (c *Cache) Label(scoresDir, evalsDir string, id int) string {
	s, e := c.Score(scoresDir, id), c.Eval(evalsDir, id)
	pick := func(get func(*Artifact) string) string {
		for _, a := range []*Artifact{e, s} {
			if a == nil {
				continue
			}
			// "unknown" is what the model emits when it could not tell, and it
			// is worse than showing nothing.
			if v := get(a); v != "" && v != "unknown" {
				return v
			}
		}
		return ""
	}
	parts := []string{}
	if v := pick(func(a *Artifact) string { return a.Company }); v != "" {
		parts = append(parts, v)
	}
	if v := pick(func(a *Artifact) string { return a.Role }); v != "" {
		parts = append(parts, v)
	}
	if len(parts) == 0 {
		return "#" + strconv.Itoa(id)
	}
	return strings.Join(parts, " — ")
}

// LifetimeAvg is the mean eval score over every successful eval on disk.
func (c *Cache) LifetimeAvg(evalsDir string) (float64, bool) {
	var sum float64
	var n int
	for _, f := range c.listDir(evalsDir) {
		if !strings.HasSuffix(f, ".json") {
			continue
		}
		if a := c.read(filepath.Join(evalsDir, f)); a != nil && a.Status == P2Evaled {
			sum += a.Score
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// Stats is the Queue tab's header line.
type Stats struct {
	Failed    int
	Gated     int
	Completed int // reached a Phase 3 verdict, tailored or deliberately skipped
	CV        int // of those, produced a tailored CV
}

// Rate is the share of finished offers that earned a CV — nil-ish when nothing
// has finished, because 0/0 is not 0%.
func (s Stats) Rate() (float64, bool) {
	if s.Completed == 0 {
		return 0, false
	}
	return float64(s.CV) / float64(s.Completed), true
}

func Lifetime(rows map[int]Row) Stats {
	var s Stats
	for _, r := range rows {
		if r.P1Status == P1Failed || r.P2Status == P2Failed || r.P3Status == P3Failed {
			s.Failed++
		}
		if r.P2Status == P2Gated {
			s.Gated++
		}
		if r.P2Status == P2Evaled && (r.P3Status == P3Completed || r.P3Status == P3Skipped) {
			s.Completed++
			if r.P3Status == P3Completed {
				s.CV++
			}
		}
	}
	return s
}

// ResultDir finds output/<date>_<slug>_<NNN> for a report number.
func (c *Cache) ResultDir(outputDir, reportNum string) (string, bool) {
	if reportNum == "" || reportNum == None {
		return "", false
	}
	suffix := "_" + reportNum
	for _, name := range c.listDir(outputDir) {
		if strings.HasSuffix(name, suffix) {
			return filepath.Join(outputDir, name), true
		}
	}
	return "", false
}

var reportFileRe = regexp.MustCompile(`^(\d{3})-.*\.md$`)

// ReportFile finds reports/<NNN>-<slug>-<date>.md for a report number.
func (c *Cache) ReportFile(reportsDir, reportNum string) (string, bool) {
	if reportNum == "" || reportNum == None {
		return "", false
	}
	for _, name := range c.listDir(reportsDir) {
		if m := reportFileRe.FindStringSubmatch(name); m != nil && m[1] == reportNum {
			return filepath.Join(reportsDir, name), true
		}
	}
	return "", false
}

var idFileRe = regexp.MustCompile(`^(\d+)\.(txt|json)$`)

// Timeline is every offer the tree knows about — one with a JD, an eval, or a
// place in the queue — in chronological order, alongside the day each first
// appeared. Ids are sequential, so numeric order is chronological.
//
// The 24-hour window this used to take is gone. The queue reads as a log with a
// heading per day, and a day you cannot scroll back to may as well not have
// happened.
//
// ponytail: the day is an artifact's mtime, so a copy or rebuild of the tree
// shifts it — the same trade Activity makes, for the same reason. It is the
// EARLIEST mtime rather than the latest on purpose: last-touch would file a
// re-run under today while its row sat in the middle of the list, and the
// grouping would print that date twice. Earliest still only bounds the day from
// above, so the clamp below is what actually makes it monotonic.
//
// ponytail: one lstat per entry in both directories, every poll — fine at a few
// hundred offers, linear in history. Cache it by the directories' own mtimes
// (see Activity) if the poll ever shows up.
func Timeline(jdsDir, evalsDir, queueFile string) ([]int, map[int]time.Time) {
	day := map[int]time.Time{}
	// Queued and not yet fetched: nothing on disk to date it, and it is being
	// worked on now.
	for _, id := range Queue(queueFile) {
		day[id] = time.Now()
	}
	for _, dir := range []string{jdsDir, evalsDir} {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			m := idFileRe.FindStringSubmatch(e.Name())
			if m == nil {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			id, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			if t, ok := day[id]; !ok || info.ModTime().Before(t) {
				day[id] = info.ModTime()
			}
		}
	}
	out := make([]int, 0, len(day))
	for id := range day {
		out = append(out, id)
	}
	sort.Ints(out)
	// Earliest-of-two is not enough on its own: an mtime is an UPPER bound on
	// when an offer appeared. A file is written at or after its offer landed, a
	// re-run rewrites the JD with today's date, and an offer the scan queued
	// yesterday but nothing fetched until this morning carries this morning.
	// Ids are handed out in arrival order, so an offer cannot have appeared
	// later than one with a higher id: clamping right to left is the tightest
	// bound that stays monotonic, which is what the day headings need.
	for i := len(out) - 2; i >= 0; i-- {
		if next := day[out[i+1]]; next.Before(day[out[i]]) {
			day[out[i]] = next
		}
	}
	return out, day
}

// ErrorText is what the row's "see error" opens. It prefers the phase's fatal()
// payload over the state row's error column, which is truncated at 200 chars.
//
// It deliberately does not read batch/logs/: both phase scripts wrote fatal()
// to stdout and only stderr to the log, so every score and eval log is 0 bytes.
// Phase 3 is the reverse — its log is the whole artifact.
func (c *Cache) ErrorText(scoresDir, evalsDir, logsDir string, id int, r Row) string {
	phase := FailedPhase(r)
	var b strings.Builder
	fmt.Fprintf(&b, "offer #%d\nphase: %s\n", id, phaseName(phase))
	if r.URL != "" {
		fmt.Fprintf(&b, "url: %s\n", r.URL)
	}
	b.WriteString("\n")

	switch phase {
	case "p1":
		if a := c.Score(scoresDir, id); a != nil && a.Error != "" {
			b.WriteString(a.Error + "\n")
			return b.String()
		}
	case "p2":
		if !r.HasReport() && r.P2Status == P2Evaled {
			b.WriteString(NoReport + "\n")
			return b.String()
		}
		if a := c.Eval(evalsDir, id); a != nil && a.Error != "" {
			b.WriteString(a.Error + "\n")
			return b.String()
		}
	case "p3":
		// Phase 3 writes its diagnosis to the log, not to a JSON payload.
		if log, err := os.ReadFile(filepath.Join(logsDir, fmt.Sprintf("pdf-%d.log", id))); err == nil && len(log) > 0 {
			b.Write(log)
			return b.String()
		}
	}
	if r.Error != "" {
		b.WriteString(r.Error + " (from local-state.tsv; truncated at 200 chars)\n")
	} else {
		b.WriteString("no error text recorded\n")
	}
	return b.String()
}

// NoReport is the diagnosis for an eval that recorded success without a report
// number — see FailedPhase.
const NoReport = "eval recorded no report number — needs a full re-run"

func phaseName(p string) string {
	switch p {
	case "p1":
		return "Phase 1 (scoring)"
	case "p2":
		return "Phase 2 (evaluation)"
	case "p3":
		return "Phase 3 (CV tailoring)"
	}
	return "none"
}
