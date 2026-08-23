// Package state owns batch/local-state.tsv: the per-offer, per-phase progress
// record that both the runner and the TUI read.
//
// The format is unchanged from the Node/bash implementation — 10 tab-separated
// columns, no quoting — so the two can share a tree during the port.
//
// It is NOT csv. The producer was `printf '%s\t...'` and the consumer
// `IFS=$'\t' read`, so a leading double quote in the error column is a literal
// quote, not a csv quote. encoding/csv would eat it. strings.Split is both
// simpler and correct here.
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const Header = "id\turl\tp1_status\tp1_score\tp1_archetype\tp2_status\tp2_report_num\tp3_status\terror\tretries"

const nCols = 10

// None is the sentinel an empty cell carries. Written, never elided — a bare
// empty cell would shift every column after it when bash read the row.
const None = "-"

// Phase 1 statuses.
const (
	P1Scored      = "scored"
	P1Failed      = "score_failed"
	P1Unavailable = "unavailable" // expired or blocked; never retried
)

// Phase 2 statuses.
const (
	P2Evaled     = "evaled"
	P2Evaluating = "evaluating"
	P2Failed     = "eval_failed"
	P2Gated      = "p1-gated"
)

// Phase 3 statuses.
const (
	P3Completed = "completed"
	P3Failed    = "pdf_failed"
	P3Skipped   = "skipped"
)

type Row struct {
	ID        int
	URL       string
	P1Status  string
	P1Score   string // kept as text: "-" is a legal value and 2.60 must not become 2.6
	Archetype string
	P2Status  string
	ReportNum string // zero-padded 3-digit, or "-"
	P3Status  string
	Error     string
	Retries   int
}

// Score parses P1Score, returning ok=false for "-" or anything unparseable.
func (r Row) Score() (float64, bool) {
	if r.P1Score == "" || r.P1Score == None {
		return 0, false
	}
	f, err := strconv.ParseFloat(r.P1Score, 64)
	return f, err == nil
}

// HasReport reports whether the row carries a usable report number. A
// successful eval always records one, so `evaled` without it is a failed eval
// wearing a success label — see FailedPhase.
func (r Row) HasReport() bool { return r.ReportNum != "" && r.ReportNum != None }

func (r Row) fields() []string {
	return []string{
		strconv.Itoa(r.ID), r.URL, dash(r.P1Status), dash(r.P1Score), dash(r.Archetype),
		dash(r.P2Status), dash(r.ReportNum), dash(r.P3Status), dash(r.Error),
		strconv.Itoa(r.Retries),
	}
}

func dash(s string) string {
	if s == "" {
		return None
	}
	return s
}

func parseRow(line string) (Row, error) {
	f := strings.Split(line, "\t")
	if len(f) != nCols {
		return Row{}, fmt.Errorf("want %d columns, got %d", nCols, len(f))
	}
	id, err := strconv.Atoi(f[0])
	if err != nil {
		return Row{}, fmt.Errorf("bad id %q", f[0])
	}
	retries, _ := strconv.Atoi(f[9]) // absent or junk means 0
	// "-" is the file's sentinel for absent; "" is the struct's. Normalising
	// here leaves exactly one representation for each, so no consumer has to
	// test both — and it makes Save∘Load an identity.
	return Row{
		ID: id, URL: f[1], P1Status: undash(f[2]), P1Score: undash(f[3]), Archetype: undash(f[4]),
		P2Status: undash(f[5]), ReportNum: undash(f[6]), P3Status: undash(f[7]),
		Error: undash(f[8]), Retries: retries,
	}, nil
}

func undash(s string) string {
	if s == None {
		return ""
	}
	return s
}

// Load reads the state file. A missing file is an empty state, not an error —
// a fresh checkout has no run history.
func Load(path string) (map[int]Row, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[int]Row{}, nil
	}
	if err != nil {
		return nil, err
	}
	rows := map[int]Row{}
	for i, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "id\t") {
			continue
		}
		r, err := parseRow(line)
		if err != nil {
			// One malformed row must not hide the other 354. The Node version
			// silently dropped these; say so instead.
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		rows[r.ID] = r
	}
	return rows, nil
}

// Save writes the file atomically: temp in the same directory, then rename. A
// crash mid-write leaves the previous state intact rather than a truncated one.
func Save(path string, rows map[int]Row) error {
	ids := make([]int, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	var b strings.Builder
	b.WriteString(Header)
	b.WriteByte('\n')
	for _, id := range ids {
		b.WriteString(strings.Join(rows[id].fields(), "\t"))
		b.WriteByte('\n')
	}
	return WriteAtomic(path, []byte(b.String()))
}

// WriteAtomic writes data to path via a same-directory temp file and a rename.
// Same-directory matters: rename across filesystems is not atomic.
func WriteAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename succeeds

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	// fsync before rename: a phase commit that survives the rename but not the
	// data is the `evaled`-with-no-report corruption this package exists to stop.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Store is the runner's handle: rows in memory, flushed atomically on commit.
// The Node version rewrote a 126 KB file per field change from bash under a
// mkdir spinlock; this holds them and writes once per commit.
type Store struct {
	mu   sync.Mutex
	path string
	rows map[int]Row
}

func Open(path string) (*Store, error) {
	rows, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, rows: rows}, nil
}

func (s *Store) Get(id int) (Row, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	return r, ok
}

// Rows returns a copy, so a caller iterating cannot race a concurrent commit.
func (s *Store) Rows() map[int]Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int]Row, len(s.rows))
	for k, v := range s.rows {
		out[k] = v
	}
	return out
}

// Commit applies mutate to the row (zero-valued but for ID if it is new) and
// flushes. Read-modify-write happens under the lock so two phase workers
// finishing at once cannot lose one another's update — the bash version's
// read-file/edit/write-file could, and its mkdir lock was what stood in for this.
func (s *Store) Commit(id int, mutate func(*Row)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		r = Row{ID: id}
	}
	mutate(&r)
	r.ID = id
	s.rows[id] = r
	return Save(s.path, s.rows)
}

// FailedPhase names the phase a row died in, or "" when it did not fail. It is
// what drives the retry flags, the error text and the debug target.
func FailedPhase(r Row) string {
	switch {
	case r.P1Status == P1Failed, r.P1Status == P1Unavailable:
		return "p1"
	case r.P2Status == P2Failed:
		return "p2"
	// `evaled` with no report number is a failed eval wearing a success label.
	// It has no report for Phase 3 to find and no failure status for a retry
	// filter to match, so uncalled it renders as "pending" against an empty
	// queue — a row the UI offers no way to act on.
	case r.P2Status == P2Evaled && !r.HasReport():
		return "p2"
	case r.P3Status == P3Failed:
		return "p3"
	}
	return ""
}

// Gated reports whether the row stopped at the Phase 1 cost gate. That is not a
// failure: the gate is a heuristic over a 4B pre-screen, and the row is asking
// a question ("proceed?"), not reporting an error.
func Gated(r Row) bool { return r.P2Status == P2Gated }
