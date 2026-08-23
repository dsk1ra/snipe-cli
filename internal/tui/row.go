package tui

import (
	"fmt"
	"strconv"

	"github.com/dsk1ra/snipe-cli/internal/state"
)

type rowKind int

const (
	kindPending rowKind = iota
	kindRunning
	kindDone
	kindGated
	kindFailed
)

// actionID names a row's inline stops. The row body itself is always the first
// stop and is not in this list.
type actionID string

const (
	actError    actionID = "error"
	actRetry    actionID = "retry"
	actDebug    actionID = "debug"
	actProceed  actionID = "proceed"
	actLink     actionID = "link"
	actDetail   actionID = "detail"
	actNoAction actionID = ""
)

type action struct {
	id    actionID
	label string
}

// rowInfo is everything the list needs about one offer, derived fresh from the
// snapshot on every render.
//
// Deriving it per render rather than caching it is what removes a whole bug
// class: the ink version kept the focused stop in a field, so a poll that
// changed a row's kind left the cursor pointing at a stop that no longer
// existed, and there was a hand-written reset patching it. Here the actions are
// recomputed and the index is clamped, so a stale index is impossible.
type rowInfo struct {
	ID       int
	Label    string
	Kind     rowKind
	Score    float64
	HasScore bool
	P1Score  float64
	HasP1    bool
	Report   string
	Link     string
	Applied  bool
	Skipped  bool
	Actions  []action
	Phase    string // the phase a failed row died in
	NoCV     bool   // evaluated, but the score did not earn a CV
	// AwaitingCV is an eval with no Phase 3 behind it at all — neither a CV nor
	// a decision not to make one.
	AwaitingCV bool
}

func describe(id int, s snapshot) rowInfo {
	r := s.rows[id]
	info := rowInfo{ID: id, Label: s.labels[id], Report: r.ReportNum}
	if v, ok := s.scores[id]; ok {
		info.Score, info.HasScore = v, true
	}
	if info.Label == "" {
		info.Label = "#" + strconv.Itoa(id)
	}
	if v, ok := r.Score(); ok {
		info.P1Score, info.HasP1 = v, true
	}
	if link, ok := s.offers[id].Link(); ok {
		info.Link = link
	}
	_, info.Applied = s.applied[id]
	_, info.Skipped = s.skipped[id]

	switch {
	case state.FailedPhase(r) != "":
		info.Kind = kindFailed
		info.Phase = state.FailedPhase(r)
		// A failed row ends at debug and drops the job link every other row
		// carries: "see error" is already a link, and the posting is not what
		// you act on next.
		info.Actions = []action{{actError, "see error"}}
		// unavailable means the posting expired or is blocked. The runner
		// refuses those, so offering a retry would be a button that cannot work.
		if r.P1Status != state.P1Unavailable {
			info.Actions = append(info.Actions, action{actRetry, "retry"})
		}
		info.Actions = append(info.Actions, action{actDebug, "debug"})

	case state.Gated(r):
		// Declining answers the question, so the row retires: it keeps its
		// score but stops asking.
		if _, declined := s.declined[id]; declined {
			info.Kind = kindDone
			info.Actions = linkOnly(info.Link)
			break
		}
		info.Kind = kindGated
		// A gated row keeps its link — unlike a failed one, the posting is
		// still worth reading before deciding to overrule the gate.
		info.Actions = append([]action{{actProceed, "proceed?"}}, linkOnly(info.Link)...)

	case r.P3Status == state.P3Completed:
		info.Kind = kindDone
		info.Actions = append([]action{{actDetail, "report"}}, linkOnly(info.Link)...)

	// An evaluated row is done on exactly the terms state.Lifetime counts as
	// completed. It used to be done as soon as the eval landed, which wore the
	// finished glyph over a Phase 3 that had not run — and made `o` answer "no
	// result folder" on the newest rows, because there genuinely was not one.
	//
	// The list still shows more done than Lifetime counts completed, by the
	// number of gate refusals above: a row you declined is finished because you
	// finished it, not because the pipeline did.
	case r.P2Status == state.P2Evaled &&
		(r.P3Status == state.P3Completed || r.P3Status == state.P3Skipped):
		info.Kind = kindDone
		// Skipped with an eval behind it means the score did not clear the
		// auto-PDF gate. Worth saying: the row is finished, not unfinished.
		// scoreText says it, in the score column.
		info.NoCV = r.P3Status == state.P3Skipped
		info.Actions = append([]action{{actDetail, "report"}}, linkOnly(info.Link)...)

	// Evaluated, with no Phase 3 behind it. It keeps its report — that much did
	// land and is worth reading now — but there is no CV and no decision not to
	// make one.
	//
	// Never "running". A runner holding the lock works the queue, and these rows
	// are not in it, so reading the lock as "this row is moving" lit four
	// abandoned rows up as in-flight while the run was busy with something else.
	// Never "pending" either, in as many words: nothing is going to pick them
	// up on its own, and a word that implies a queue position they do not have
	// is what sent somebody looking for a run that had stalled.
	case r.P2Status == state.P2Evaled:
		info.Kind = kindPending
		info.AwaitingCV = true
		info.Actions = append([]action{{actDetail, "report"}}, linkOnly(info.Link)...)

	case r.P1Status == state.P1Scored, r.P2Status == state.P2Evaluating:
		info.Kind = kindRunning
		info.Actions = linkOnly(info.Link)

	default:
		info.Kind = kindPending
		info.Actions = linkOnly(info.Link)
	}
	return info
}

func linkOnly(link string) []action {
	if link == "" {
		return nil
	}
	return []action{{actLink, "link"}}
}

// stops is the number of focusable positions on the row: the row body plus each
// inline action.
func (r rowInfo) stops() int { return 1 + len(r.Actions) }

// actionAt maps a focus index to an action, returning actNoAction for index 0
// (the row body).
func (r rowInfo) actionAt(i int) actionID {
	if i <= 0 || i > len(r.Actions) {
		return actNoAction
	}
	return r.Actions[i-1].id
}

// inFlight reports that the pipeline has this row OPEN: started and not
// finished. Scored with no eval, mid-evaluation, or evaluated with no Phase 3
// verdict behind it.
//
// Whether that means "being worked on" or "stopped half way" is not something
// the row can know — it depends on whether a run holds the lock — so the two
// are told apart where that is known, at the render.
func (r rowInfo) inFlight() bool { return r.Kind == kindRunning || r.AwaitingCV }

// marker is the one glyph that says where the offer stands, coloured.
//
// It replaced a status column that spelled out "running", "pending" and "no CV"
// in words. The words cost every row in the window the width of the longest of
// them, to repeat what this column and the score already say: a bare score is a
// finished offer, "P2 2.9" is one the Phase 3 gate turned back, no score at all
// is one nothing has touched. What the words did carry that a tick could not is
// motion, and a tick cannot show motion — so the render swaps in a spinner while
// a run actually holds the lock, and this stays the still frame of it.
func (r rowInfo) marker() string {
	switch {
	case r.Kind == kindFailed:
		return styErr.Render("✗")
	case r.Kind == kindGated:
		// A question, and the row asks it again in its actions ("proceed?").
		return styAccent.Render("?")
	case r.Kind == kindDone && r.NoCV:
		// Finished, and deliberately without a CV. Dim rather than a glyph of
		// its own: the row is done, and the score column says on what terms.
		return styDim.Render("✓")
	case r.Kind == kindDone:
		return styOK.Render("✓")
	case r.inFlight():
		// Open, with nothing moving it — a paused run, a killed one, or a Phase
		// 3 that never ran. Warn rather than error: nothing failed, but nothing
		// will pick it up either, and ▶ is what it wants.
		return styWarn.Render("!")
	}
	// Queued, untouched. It has a turn coming and needs nothing from anybody.
	return styDim.Render("·")
}

// mark is the triage note, in its own reserved column beside the marker.
//
// A property of the row, not an outcome of the run — which is why it sits
// alongside the glyph instead of replacing it. Overwriting it lost the outcome:
// an applied offer that had produced a CV rendered identically to an applied one
// that had failed. The column is reserved when unmarked so every label starts at
// the same offset either way.
func (r rowInfo) mark() string {
	switch {
	case r.Applied:
		return " >"
	case r.Skipped:
		return " -"
	}
	return "  "
}

// scoreText is the number the row is judged on, and the one column a reader
// scans first.
//
// A bare number is a score that went all the way. A prefix names the phase that
// produced it AND stopped there: "P1 2.0" is a pre-score the Phase 2 gate turned
// back, "P2 2.8" an eval the Phase 3 threshold turned back. Both are banded in
// the same colours as every other number in the list, so the column still reads
// as one column.
//
// The prefix replaced a "below threshold" status column, which spent seventeen
// columns of every row in the window — the widths are measured across the window,
// so one skipped row narrowed twenty labels — to say what the score already says
// once you know which phase it came from. A row with no score at all shows
// nothing rather than a placeholder, because "not yet" is not a score.
func (r rowInfo) scoreText() string {
	switch {
	case r.HasScore:
		s := scoreStyle(r.Score).Render(fmt.Sprintf("%.1f", r.Score))
		if r.NoCV {
			return styDim.Render("P2 ") + s
		}
		return s
	case r.HasP1:
		return styDim.Render("P1 ") + scoreStyle(r.P1Score).Render(fmt.Sprintf("%.1f", r.P1Score))
	}
	return ""
}
