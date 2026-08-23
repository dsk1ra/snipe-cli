package state

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Buckets counts activity per day and per hour, the two resolutions the
// activity grid renders.
type Buckets struct {
	Days  map[string]int // "2026-08-22"
	Hours map[string]int // "2026-08-22:14"
}

func newBuckets() Buckets {
	return Buckets{Days: map[string]int{}, Hours: map[string]int{}}
}

func DayKey(t time.Time) string { return t.Format("2006-01-02") }

func (b Buckets) add(t time.Time) {
	k := DayKey(t)
	b.Days[k]++
	b.Hours[k+":"+itoa(t.Hour())]++
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func (b Buckets) Total() int {
	n := 0
	for _, v := range b.Days {
		n += v
	}
	return n
}

// SumPrefix totals every day whose key starts with prefix — "2026-" for a year,
// "2026-08" for a month.
func (b Buckets) SumPrefix(prefix string) int {
	n := 0
	for k, v := range b.Days {
		if strings.HasPrefix(k, prefix) {
			n += v
		}
	}
	return n
}

// Best is the busiest day matching prefix.
func (b Buckets) Best(prefix string) (string, int) {
	var bk string
	var bv int
	for k, v := range b.Days {
		if strings.HasPrefix(k, prefix) && (bk == "" || v > bv) {
			bk, bv = k, v
		}
	}
	return bk, bv
}

// Streak counts consecutive days back from today. Today being still empty does
// not break it — the day is not over.
func (b Buckets) Streak(now time.Time) int {
	d := now
	if b.Days[DayKey(d)] == 0 {
		d = d.AddDate(0, 0, -1)
	}
	n := 0
	for b.Days[DayKey(d)] > 0 {
		n++
		d = d.AddDate(0, 0, -1)
	}
	return n
}

// Activity is the pair the grid switches between.
type Activity struct {
	Scans Buckets
	Apps  Buckets
}

type cachedActivity struct {
	evalsMtime time.Time
	appliedMod time.Time
	act        Activity
}

// Activity builds both bucket sets, recomputing only when the evals directory
// or applied.tsv has actually moved.
//
// ponytail: an eval's mtime is its scan date, so a copy or rebuild of the tree
// shifts history. reports/ filenames stay the durable record — this is the
// cheap view, not the archive.
func (c *Cache) Activity(evalsDir, appliedFile string) Activity {
	var evalsMod, appliedMod time.Time
	if st, err := os.Stat(evalsDir); err == nil {
		evalsMod = st.ModTime()
	}
	if st, err := os.Stat(appliedFile); err == nil {
		appliedMod = st.ModTime()
	}

	c.mu.Lock()
	if c.act != nil && c.act.evalsMtime.Equal(evalsMod) && c.act.appliedMod.Equal(appliedMod) {
		a := c.act.act
		c.mu.Unlock()
		return a
	}
	c.mu.Unlock()

	act := Activity{Scans: newBuckets(), Apps: newBuckets()}
	if ents, err := os.ReadDir(evalsDir); err == nil {
		for _, e := range ents {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			if info, err := e.Info(); err == nil {
				act.Scans.add(info.ModTime())
			}
		}
	}
	for _, ts := range LoadMarks(appliedFile) {
		act.Apps.add(ts.Local())
	}

	c.mu.Lock()
	c.act = &cachedActivity{evalsMtime: evalsMod, appliedMod: appliedMod, act: act}
	c.mu.Unlock()
	return act
}

// ReportsPerDay is the durable counterpart to Scans: report filenames carry
// their own date, so a rebuilt tree does not move them.
func ReportsPerDay(reportsDir string) Buckets {
	b := newBuckets()
	ents, err := os.ReadDir(reportsDir)
	if err != nil {
		return b
	}
	for _, e := range ents {
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if len(name) < 10 {
			continue
		}
		// <NNN>-<slug>-<YYYY-MM-DD>
		date := name[len(name)-10:]
		if t, err := time.Parse("2006-01-02", date); err == nil {
			b.add(t)
		}
	}
	return b
}
