package ollama

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// TimingLog appends one row per model call to a TSV.
//
// There was no per-phase wall clock anywhere before this existed, so the "5
// minutes per JD" budget was unfalsifiable and every latency claim was a guess.
//
// load_duration is the load-event detector: a call that found its model
// resident reports single-digit ms, one that evicted another model reports
// seconds. Counting the big ones counts the thrash.
type TimingLog struct {
	mu   sync.Mutex
	path string
}

const TimingHeader = "ts\tid\tphase\tmodel\ttotal_s\tload_s\tprompt_tokens\tprompt_s\teval_tokens\teval_s\tdone_reason\textra"

// NewTimingLog returns nil when path is empty, and every method is nil-safe —
// so instrumentation costs one branch when it is off and no call site needs a
// guard.
func NewTimingLog(path string) *TimingLog {
	if path == "" {
		return nil
	}
	return &TimingLog{path: path}
}

const nsPerS = 1e9

func secs(ns int64) float64 { return float64(ns) / nsPerS }

func (t *TimingLog) log(phase, model string, id int, extra string, m metrics) {
	if t == nil {
		return
	}
	idStr := "-"
	if id > 0 {
		idStr = fmt.Sprint(id)
	}
	if extra == "" {
		extra = "-"
	}
	reason := m.DoneReason
	if reason == "" {
		reason = "-"
	}
	t.append(fmt.Sprintf("%s\t%s\t%s\t%s\t%.3f\t%.3f\t%d\t%.3f\t%d\t%.3f\t%s\t%s\n",
		time.Now().Format(time.RFC3339), idStr, phase, model,
		secs(m.TotalDuration), secs(m.LoadDuration),
		m.PromptEvalCount, secs(m.PromptEvalDuration),
		m.EvalCount, secs(m.EvalDuration), reason, extra))
}

// Wall records something that is not a model call — a whole phase, a PDF
// render. Ollama's own durations miss JD fetching, LaTeX and process startup,
// and the budget is measured on the wall, not on the GPU.
func (t *TimingLog) Wall(phase string, id int, d time.Duration) {
	if t == nil {
		return
	}
	t.log(phase, "wall", id, "-", metrics{
		TotalDuration: int64(d), DoneReason: "wall",
	})
}

func (t *TimingLog) append(row string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f, err := os.OpenFile(t.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return // telemetry never breaks a run
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		f.WriteString(TimingHeader + "\n")
	}
	f.WriteString(row)
}
