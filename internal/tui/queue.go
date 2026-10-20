package tui

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/tracker"
)

type qFocus int

const (
	fNone qFocus = iota
	fJD
	fURL
	fCmd
	fFind
	fAdd
	fPlay
	fList
)

// typing is "this focus owns the keyboard": every printable key and the arrows
// go to its text input, and the root never sees them.
func (f qFocus) typing() bool { return f == fJD || f == fURL || f == fCmd || f == fFind }

type queueTab struct {
	l     paths.Layout
	cache *state.Cache
	keys  queueKeys
	snap  snapshot
	goals config.Goals

	focus     qFocus
	lastInput qFocus // where Tab re-enters the input group
	jd, url   textinput.Model
	// cmd is the / palette: the CLI subcommands that have no key because they
	// are not about a row. busy names the one in flight, for the pane heading —
	// a scan is the one action here that takes long enough to wonder about.
	cmd  textinput.Model
	busy string
	// find is the fuzzy filter over the list: type the letters you remember, in
	// the order you remember them, and every row whose label contains them as a
	// subsequence survives — "cco" finds Cisco.
	//
	// visible is what it leaves, and it is what every index in this tab means:
	// cursor, scroll and day() are all offsets into it, never into the session.
	// One slice rather than a filter applied at render time, because a cursor
	// and a list that disagree about which row is row 3 is the whole bug class.
	find    textinput.Model
	visible []int

	cursor int // index into snap.session
	act    int // 0 = the row body; 1..n = its inline actions
	// scroll is the row one PAST the last visible one. The list is anchored to
	// the bottom, so what stays put as it scrolls is the end of the window, not
	// its start — and the start cannot be the anchor anyway, since how many rows
	// fit depends on how many day headings fall between them.
	scroll int

	width, height int
	listWindow    int
	frame         int

	// due answers "how many follow-ups are actionable", asked of the tab that
	// owns the data rather than copied from it. nil before the root binds it.
	due func() int

	// review is the skills screen, open over the list when non-nil.
	review *skillsReview

	detail      viewport.Model
	detailOpen  bool
	detailTitle string
	// detailBody is the report as it was read off disk. Kept because the laid
	// out version is only true at one width, and the window resizes.
	detailBody string
}

// detailMsg carries text into the reader pane. Reading a report inside the tool
// is the one thing the ink version could not do at all — o, "see error" and
// debug all shelled out to xdg-open.
type detailMsg struct {
	title string
	body  string
	err   error
}

func newQueueTab(l paths.Layout, c *state.Cache, g config.Goals) *queueTab {
	jd := textinput.New()
	jd.Prompt = "jd  "
	jd.Placeholder = "paste a job description — or / for a command"
	url := textinput.New()
	url.Prompt = "url "
	url.Placeholder = "https://… (optional, for tracking)"
	cmd := textinput.New()
	cmd.Prompt = ""
	find := textinput.New()
	find.Prompt = ""

	return &queueTab{
		l: l, cache: c, keys: newQueueKeys(), goals: g,
		focus: fNone, lastInput: fJD,
		jd: jd, url: url, cmd: cmd, find: find,
		detail: viewport.New(),
	}
}

func (q *queueTab) setSize(w, h int) {
	q.width, q.height = w, h
	q.jd.SetWidth(w - 6)
	q.url.SetWidth(w - 6)
	q.detail.SetWidth(w)
	q.detail.SetHeight(h - 2)
	// The text was wrapped to the old width, so it is wrong at the new one.
	// Re-laid rather than re-read: the file has not changed, only the pane.
	q.layOutDetail()
}

// layOutDetail wraps the open report to the reader's current width, tables and
// all. The scroll position is left alone — the viewport clamps it on render, so
// a resize lands near where the reader was rather than back at the top.
func (q *queueTab) layOutDetail() {
	if q.detailBody == "" {
		return
	}
	q.detail.SetContent(renderReport(q.detailBody, q.detail.Width()))
}

func (q *queueTab) setSnapshot(s snapshot) {
	q.snap = s
	q.refilter()
}

// refilter rebuilds the visible rows from whatever find holds. Called from the
// two places either side of it can move — a poll and a keystroke — so there is
// one expression deciding what is on screen.
func (q *queueTab) refilter() {
	pat := strings.TrimSpace(q.find.Value())
	if pat == "" {
		q.visible = q.snap.session
		q.clampCursor()
		return
	}
	// A fresh slice, never session re-sliced: they alias, and filtering in place
	// would truncate the snapshot the other tabs are reading.
	out := make([]int, 0, len(q.snap.session))
	for _, id := range q.snap.session {
		if subseq(q.snap.labels[id], pat) {
			out = append(out, id)
		}
	}
	q.visible = out
	q.clampCursor()
}

// subseq is the match: every rune of pat appears in s, in order, case-folded.
//
// ponytail: no scoring, no ranking. The list is grouped by day and reads
// chronologically, and re-ordering it by match quality would cost the grouping
// to save a keystroke. Reach for a ranked matcher when the filter routinely
// leaves more rows than fit.
func subseq(s, pat string) bool {
	s, pat = strings.ToLower(s), strings.ToLower(pat)
	for _, r := range pat {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+utf8.RuneLen(r):]
	}
	return true
}

// clampCursor keeps the cursor and the action index inside what currently
// exists. Everything derives from the snapshot, so a poll that changes a row's
// kind cannot leave the focus pointing at a stop that has gone.
func (q *queueTab) clampCursor() {
	n := len(q.visible)
	if n == 0 {
		q.cursor, q.act, q.scroll = 0, 0, 0
		return
	}
	if q.cursor >= n {
		q.cursor = n - 1
	}
	if q.cursor < 0 {
		q.cursor = 0
	}
	if max := q.current().stops() - 1; q.act > max {
		q.act = max
	}
	if q.act < 0 {
		q.act = 0
	}
}

func (q *queueTab) current() rowInfo {
	if len(q.visible) == 0 {
		return rowInfo{}
	}
	return describe(q.visible[q.cursor], q.snap)
}

// Update reports whether it consumed the message. Returning false is real
// delegation: the root then gets its shot at the same key, which is how ← at a
// row's left edge falls through to a tab switch and how `q` still quits from
// everywhere the tab is not typing. The ink version simulated this with a
// handler that returned false and a pair of hand-written guards.
func (q *queueTab) Update(m tea.Msg) (bool, tea.Cmd) {
	switch m := m.(type) {
	case tea.PasteMsg:
		// Native bracketed paste. The ink version hand-parsed the \x1b[200~
		// framing, including the case where a chunk boundary split the marker.
		return true, q.onPaste(m.Content)

	case reviewMsg:
		if m.err != nil {
			return true, failf("%v", m.err)
		}
		q.review = m.r
		return true, nil

	case reviewDoneMsg:
		if q.review == nil {
			return true, nil
		}
		if m.err != nil {
			return true, failf("%v", m.err)
		}
		q.review.done(m)
		return true, notef("%s", m.note)

	case detailMsg:
		q.busy = ""
		if m.err != nil {
			return true, failf("%v", m.err)
		}
		q.detailOpen = true
		q.detailTitle = m.title
		q.detailBody = m.body
		q.layOutDetail()
		q.detail.GotoTop()
		return true, nil

	case tea.KeyPressMsg:
		return q.onKey(m)
	}
	return false, nil
}

// urlFieldLimit is how many whitespace-separated pieces a paste may have and
// still be filed as a link.
//
// Deliberately generous. The guard below exists to catch a job description
// landing on a one-line field, which is unmistakable at several hundred words —
// everything between "one token" and "prose" is left where the cursor put it,
// because moving somebody's paste on a hunch is worse than the paste being in
// the wrong box where they can see it.
const urlFieldLimit = 8

// onPaste files a paste by what it IS, not only by where the cursor is.
//
// Routing on focus alone put a 1,884-character posting on the URL line, and a
// link pasted into the JD box would have been submitted as the job description
// itself — 400 characters of tracking parameters is well past the 100-character
// floor that catches an empty paste, so nothing downstream would have objected.
//
// Both guards fire only on the unambiguous case and say what they did. A paste
// that quietly lands somewhere else is worse than one that lands in the wrong
// place.
func (q *queueTab) onPaste(text string) tea.Cmd {
	if q.focus == fURL {
		if fields := strings.Fields(text); len(fields) <= urlFieldLimit {
			q.url.SetValue(q.url.Value() + strings.Join(fields, " "))
			return nil
		}
		q.pasteJD(text)
		return notef("that is a description, not a link — it went to the JD field")
	}

	if isLink(text) {
		// Replaced, not appended: a whole link arrived, and two of them
		// concatenated is neither.
		q.url.SetValue(strings.TrimSpace(text))
		q.setFocus(fURL)
		return notef("that is a link — it went to the URL field")
	}
	q.pasteJD(text)
	return nil
}

// isLink is one token that parses with a scheme and a host.
//
// Strict on purpose: a bare "linkedin.com/jobs/123" reads as a link to a person
// and as a relative path to net/url, and it stays in the JD field rather than
// being moved on a guess. The one-token test is what does the real work — a job
// description is never one token.
func isLink(text string) bool {
	f := strings.Fields(text)
	if len(f) != 1 {
		return false
	}
	u, err := url.Parse(f[0])
	return err == nil && u.Scheme != "" && u.Host != ""
}

func (q *queueTab) pasteJD(text string) {
	q.setFocus(fJD)
	if v := q.jd.Value(); v != "" {
		q.jd.SetValue(v + "\n" + text)
		return
	}
	q.jd.SetValue(text)
}

func (q *queueTab) onKey(k tea.KeyPressMsg) (bool, tea.Cmd) {
	// The skills screen is modal like the reader pane, and closes the same way.
	if q.review != nil {
		switch k.String() {
		case "ctrl+c":
			return false, nil
		case "esc", "q", "v":
			q.review = nil
			return true, nil
		}
		return true, q.review.key(k)
	}
	// The reader pane owns every key while it is open, bar the ones that close
	// it. ctrl+c is the exception: a modal must never trap a quit.
	if q.detailOpen {
		switch k.String() {
		case "ctrl+c":
			return false, nil
		case "esc", "q", "v":
			q.detailOpen = false
			return true, nil
		}
		var cmd tea.Cmd
		q.detail, cmd = q.detail.Update(k)
		return true, cmd
	}

	// Tab always cycles groups, wherever focus is — including out of a field.
	if key.Matches(k, q.keys.Cycle) {
		q.cycle()
		return true, nil
	}

	// / opens the command palette from anywhere the keyboard is free. Guarded on
	// typing() rather than placed after it, because a slash typed into a job
	// description is a slash.
	if !q.focus.typing() && key.Matches(k, q.keys.Slash) {
		q.setFocus(fCmd)
		return true, nil
	}

	// f opens the filter over the list. Guarded on typing() for the same reason
	// as the slash: an f typed into a job description is an f.
	if !q.focus.typing() && key.Matches(k, q.keys.Find) {
		q.setFocus(fFind)
		return true, nil
	}

	// A focused text field claims every printable key and the arrows. This is
	// delegation, not a guard: the root never sees these, so `q` still quits
	// from everywhere else without a typing() test.
	if q.focus.typing() {
		if k.String() == "ctrl+c" {
			return false, nil
		}
		return true, q.typingKey(k)
	}

	switch {
	case key.Matches(k, q.keys.Out):
		// A filter is the first thing esc undoes. It survives leaving the field
		// — that is the point, you filter and then act on what is left — so the
		// key that steps out of the list is also the key that lets it go.
		if q.find.Value() != "" {
			q.find.SetValue("")
			q.refilter()
			return true, nil
		}
		if q.focus == fNone {
			return false, nil
		}
		q.setFocus(fNone)
		return true, nil

	case key.Matches(k, q.keys.Down):
		return true, q.moveDown()

	case key.Matches(k, q.keys.Up):
		if q.focus != fList {
			// Add is the one non-typing stop in the composer, and ↑ off it goes
			// back to the URL rather than nowhere.
			if f := composerStep(q.focus, false); f != fNone {
				q.setFocus(f)
				return true, nil
			}
			return false, nil
		}
		return true, q.moveUp()

	case key.Matches(k, q.keys.Fire):
		if q.focus == fNone {
			return false, nil
		}
		return true, q.fire()
	}

	// ←→ walk the focused row's inline stops. Running off either edge is not
	// handled, so the root switches tabs — which is what a row with no inline
	// actions did before.
	if q.focus == fList && len(q.visible) > 0 {
		switch {
		case key.Matches(k, q.keys.NextAct):
			if q.act < q.current().stops()-1 {
				q.act++
				return true, nil
			}
			return false, nil
		case key.Matches(k, q.keys.PrevAct):
			if q.act > 0 {
				q.act--
				return true, nil
			}
			return false, nil
		}
	}

	// Row-scoped single keys, live only while the list has focus.
	if q.focus != fList || len(q.visible) == 0 {
		return false, nil
	}
	info := q.current()
	switch {
	case key.Matches(k, q.keys.Open):
		return true, q.openResult(info)
	case key.Matches(k, q.keys.Skills):
		return true, openSkills(q.l, info)
	case key.Matches(k, q.keys.Applied):
		return true, q.markApplied(info)
	case key.Matches(k, q.keys.Skip):
		return true, q.mark(q.l.Skipped(), q.snap.skipped, info.ID, "skipped")
	}

	// proceed? is a question, so it takes an answer as well as Enter — but only
	// while it is the focused stop. No new global hotkeys, and `n` types
	// nowhere else.
	if info.actionAt(q.act) == actProceed {
		switch {
		case key.Matches(k, q.keys.Yes):
			return true, q.proceed(info)
		case key.Matches(k, q.keys.No):
			return true, q.decline(info)
		}
	}
	return false, nil
}

func (q *queueTab) typingKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		// Enter walks the input chain rather than submitting: jd → url → add.
		// The palette is not part of that chain — it runs.
		switch q.focus {
		case fCmd:
			return q.runCommand(&q.cmd)
		case fFind:
			// The filter stays on; Enter is "now let me act on these".
			q.setFocus(fList)
		case fJD:
			// A field holding nothing but a command IS a command. Walking on to
			// ask for its URL, and then refusing it at the end for being five
			// characters long, is three keys spent on a typo.
			if isCommand(q.jd.Value()) {
				return q.runCommand(&q.jd)
			}
			q.setFocus(fURL)
		default:
			q.setFocus(fAdd)
		}
		return nil
	case "esc":
		q.field().SetValue("")
		if q.focus == fFind {
			q.refilter()
		}
		q.setFocus(fNone)
		return nil
	case "backspace":
		// The / that opens the palette is drawn, not stored, so backspace over an
		// empty command had nothing to erase and the slash could only be taken
		// back by esc — which drops you out of the composer entirely. Erasing it
		// hands the keyboard to the JD field, where the something-else goes.
		if q.focus == fCmd && q.cmd.Value() == "" {
			q.setFocus(fJD)
			return nil
		}
		if q.focus == fFind && q.find.Value() == "" {
			q.setFocus(fNone)
			return nil
		}
	case "up", "down":
		// A single-line textinput does nothing with ↑↓, so they were being
		// swallowed rather than used. Walking the composer with them is what
		// makes Enter's chain reversible from inside a field.
		if f := composerStep(q.focus, k.String() == "down"); f != fNone {
			q.setFocus(f)
			return nil
		}
	}
	var cmd tea.Cmd
	*q.field(), cmd = q.field().Update(k)
	if q.focus == fFind {
		q.refilter()
	}
	return cmd
}

// composerStep is the neighbour above or below f in the composer, or fNone at
// either end.
//
// The composer reads top to bottom — the JD field, the URL under it, then Add —
// and this is the same chain Enter walks forward. Without a way back, Enter on
// the JD field was one-way: you landed on the URL, then on Add, and nothing but
// three Tabs round the whole cycle returned you to the description you had just
// pasted.
//
// ▶ is not in the chain. It sits BESIDE the JD field rather than under it, so
// it is not somewhere ↑ or ↓ should land; Tab still reaches it.
func composerStep(f qFocus, down bool) qFocus {
	switch {
	case down && f == fJD:
		return fURL
	case down && f == fURL:
		return fAdd
	case !down && f == fURL:
		return fJD
	case !down && f == fAdd:
		return fURL
	}
	return fNone
}

// field is whichever input currently owns the keyboard. One accessor rather
// than the same three-way branch spelled out at every call site.
func (q *queueTab) field() *textinput.Model {
	switch q.focus {
	case fJD:
		return &q.jd
	case fCmd:
		return &q.cmd
	case fFind:
		return &q.find
	}
	return &q.url
}

func (q *queueTab) setFocus(f qFocus) {
	// The palette is deliberately not remembered: Tab re-enters the composer,
	// and a tab cycle that lands back on a half-typed command is not a cycle
	// anybody asked for.
	if q.focus == fJD || q.focus == fURL {
		q.lastInput = q.focus
	}
	q.jd.Blur()
	q.url.Blur()
	q.cmd.Blur()
	q.find.Blur()
	q.focus = f
	switch f {
	case fJD:
		q.jd.Focus()
	case fURL:
		q.url.Focus()
	case fCmd:
		q.cmd.Focus()
	case fFind:
		q.find.Focus()
	case fList:
		if len(q.visible) > 0 {
			q.cursor = len(q.visible) - 1 // start at the most recent
			q.act = 0
			q.clampCursor()
		}
	}
}

// cycle moves between the three groups: inputs (re-entering at the last field
// used) → ▶ → the list.
func (q *queueTab) cycle() {
	switch q.focus {
	case fNone:
		q.setFocus(q.lastInput)
	case fJD, fURL, fCmd, fFind, fAdd:
		q.setFocus(fPlay)
	case fPlay:
		q.setFocus(fList)
	default:
		q.setFocus(q.lastInput)
	}
}

// focusDay puts the list on the LAST row of a day, reporting whether it found
// one.
//
// The last rather than the first: the window fills upward from the cursor, so
// landing on the end of a day is what puts the whole of that day on screen. The
// cursor is set after setFocus deliberately here — setFocus opens the list on
// the newest row, and this is the one caller that knows better.
func (q *queueTab) focusDay(key string) (tea.Cmd, bool) {
	// A filter left over from earlier would hide the very day being opened, so
	// it goes: this is a jump from another tab, not a refinement of this one.
	q.find.SetValue("")
	q.refilter()
	n := 0
	last := -1
	for i := range q.visible {
		if q.day(i) == key {
			n++
			last = i
		}
	}
	if last < 0 {
		return failf("nothing in the queue for %s", key), false
	}
	q.setFocus(fList)
	q.cursor, q.act, q.scroll = last, 0, last+1
	return notef("%s — %d in the queue", key, n), true
}

func (q *queueTab) moveDown() tea.Cmd {
	if q.focus != fList {
		// ↓ dives into the list from anywhere above it, landing wherever
		// setFocus puts it — which is the newest row.
		//
		// This used to reset the cursor to 0 straight after, so the two ways in
		// disagreed: Tab entered at the newest row and ↓ entered at the oldest
		// offer on record. ↓ is the one people press, and it left them holding
		// the key through every JD they had ever evaluated.
		q.setFocus(fList)
		return nil
	}
	if q.cursor < len(q.visible)-1 {
		q.cursor++
		q.act = 0
		q.clampCursor()
	}
	return nil
}

func (q *queueTab) moveUp() tea.Cmd {
	if q.focus != fList {
		return nil
	}
	if q.cursor > 0 {
		q.cursor--
		q.act = 0
		q.clampCursor()
		return nil
	}
	q.setFocus(fNone) // ↑ off the top steps back out
	return nil
}

// fire acts on whatever is focused. One switch, not five special cases in the
// key handler.
func (q *queueTab) fire() tea.Cmd {
	switch q.focus {
	case fNone:
		q.setFocus(q.lastInput)
		return nil
	case fAdd:
		return q.enqueue()
	case fPlay:
		return q.start()
	case fList:
		if len(q.visible) == 0 {
			return nil
		}
		info := q.current()
		switch info.actionAt(q.act) {
		case actLink:
			return openExternal(info.Link)
		case actError:
			return q.openError(info)
		case actRetry:
			return q.retry(info)
		case actProceed:
			return q.proceed(info)
		case actDebug:
			return q.openDebug(info)
		case actSkills:
			return openSkills(q.l, info)
		default:
			return q.openResult(info)
		}
	}
	return nil
}

func (q *queueTab) enqueue() tea.Cmd {
	// Checked before enqueueJD, not inside it: the JD length floor is a real
	// guard on a real job description, and a command is not one of those. This
	// is the other way the composer can be submitted — Tab to the button and
	// press Enter — and it must answer the same as Enter in the field.
	if isCommand(q.jd.Value()) {
		return q.runCommand(&q.jd)
	}
	id, err := enqueueJD(q.l, q.jd.Value(), strings.TrimSpace(q.url.Value()))
	if err != nil {
		return failf("%v", err)
	}
	q.jd.SetValue("")
	q.url.SetValue("")
	q.setFocus(fJD)

	// Adding never starts anything. The pipeline runs only when somebody asks
	// for it — ▶, or the run already holding the lock, which drains the queue
	// when its current offer finishes.
	if err := appendQueue(q.l.Queue(), id); err != nil {
		return failf("queueing #%d: %v", id, err)
	}
	if q.snap.running {
		return notef("queued #%d — the active run picks it up", id)
	}
	return notef("queued #%d — ▶ runs it", id)
}

// paletteCmds is what / can reach: this binary's own subcommands that have no
// key, because they are not about a row.
//
// A named set rather than a shell. Everything after the name is passed straight
// through, so a subcommand's own flags work — `/scan -company acme`,
// `/scan -dry-run` — without the palette knowing any of them.
var paletteCmds = []string{"scan", "doctor", "status"}

// parseCommand maps a typed line onto that argv, or says why it cannot.
func parseCommand(line string) ([]string, error) {
	f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "/"))
	if len(f) == 0 {
		return nil, nil // nothing typed: Enter is a cancel
	}
	name := strings.TrimPrefix(f[0], "/")
	for _, ok := range paletteCmds {
		if name == ok {
			return append([]string{name}, f[1:]...), nil
		}
	}
	return nil, fmt.Errorf("no command %q — try /%s", name, strings.Join(paletteCmds, ", /"))
}

// isCommand reports whether a field holds nothing but a command line.
//
// One line, opening with a slash. That is unambiguous in both directions: a job
// description is never one line and never starts with one, so nothing typed here
// has to be guessed at. An unknown name still counts — `/scna` is a mistyped
// command, and saying so is a better answer than filing it as a job description
// and refusing it for being five characters long.
func isCommand(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "/") && !strings.Contains(s, "\n")
}

// runCommand fires whatever the given field holds and returns the focus.
//
// It takes the field rather than its text so that a line that does not parse
// stays where it was typed, to be corrected rather than retyped — and so that
// the field cleared is the one the command came from. The palette can be open
// over a job description that is still staged, and clearing both would be
// throwing that away to run a scan.
//
// The output comes back into the reader pane rather than the footer: these are
// commands whose whole point is what they print, and a scan that says "3 new"
// and nothing about the 200 it dropped is a scan you have to run again in a
// terminal to understand.
func (q *queueTab) runCommand(in *textinput.Model) tea.Cmd {
	argv, err := parseCommand(in.Value())
	if err != nil {
		return failf("%v", err)
	}
	in.SetValue("")
	q.setFocus(fNone)
	if argv == nil {
		return nil
	}
	q.busy = argv[0] + "…"
	// Sequenced, not batched: the re-read has to happen after the command has
	// written whatever it queued, not alongside it.
	return tea.Sequence(runSubcommand(q.l, argv), refresh())
}

// start is both start and resume: the checkpoints decide where the work picks
// up, so there is no separate resume command and nothing to get wrong.
func (q *queueTab) start() tea.Cmd {
	if q.snap.running {
		return failf("already running (PID %d) — s stops it", q.snap.pid)
	}
	if len(q.snap.queue) == 0 {
		return startRunner(q.l, runnerArgs{note: "running the pipeline"})
	}
	return startRunner(q.l, runnerArgs{
		args: []string{"--drain"},
		note: fmt.Sprintf("draining %d queued", len(q.snap.queue)),
	})
}

// retry re-runs a failed offer. --retry-failed is what overrides all three
// phase guards: without it a row that is scored+evaled with no report skips
// Phases 1–2 and fails Phase 3 forever.
func (q *queueTab) retry(info rowInfo) tea.Cmd {
	if q.snap.running {
		return failf("a run is active — s stops it first")
	}
	return startRunner(q.l, runnerArgs{
		args: []string{"--only-id", strconv.Itoa(info.ID), "--retry-failed"},
		note: fmt.Sprintf("retrying #%d from %s", info.ID, info.Phase),
	})
}

// proceed overrules the Phase 1 cost gate. Nothing is re-scored: the cached
// score and JD are reused and the run starts at Phase 2.
func (q *queueTab) proceed(info rowInfo) tea.Cmd {
	if q.snap.running {
		return failf("a run is active — s stops it first")
	}
	return startRunner(q.l, runnerArgs{
		args: []string{"--only-id", strconv.Itoa(info.ID), "--p1-threshold", "0"},
		note: fmt.Sprintf("evaluating #%d despite the gate", info.ID),
	})
}

// decline answers the gate "no". Nothing on the pipeline changes — the row was
// already finished; it just stops asking. Undo is deleting the line.
func (q *queueTab) decline(info rowInfo) tea.Cmd {
	q.snap.declined[info.ID] = time.Now()
	if err := q.snap.declined.Save(q.l.Declined()); err != nil {
		return failf("%v", err)
	}
	q.act = 0
	return notef("#%d dismissed", info.ID)
}

// markApplied toggles the mark AND the tracker row behind it. The mark file is
// a queue-local flag; data/applications.md's Status cell is what the follow-up
// cadence reads, and it stayed "Evaluated" — so an offer marked applied here
// never entered the follow-up list at all.
//
// Only the Evaluated↔Applied pair moves. A row that has already progressed
// (Responded, Interview, Rejected) is not walked backwards by a toggle here,
// and an offer with no tracker row yet just keeps its mark.
func (q *queueTab) markApplied(info rowInfo) tea.Cmd {
	on, err := toggleMark(q.l.Applied(), q.snap.applied, info.ID)
	if err != nil {
		return failf("%v", err)
	}
	known, err := q.syncTracker(info.Report, on)
	if err != nil {
		return failf("%v", err)
	}
	switch {
	case !on:
		return notef("#%d unmarked", info.ID)
	case !known:
		// Below-threshold rows never reach Phase 3, so the merge never wrote
		// them a row. Applying to one by hand is legitimate; silently leaving
		// it out of the cadence is not.
		return notef("#%d marked applied — no tracker row, so no follow-ups for it", info.ID)
	}
	return notef("#%d marked applied", info.ID)
}

// syncTracker reports whether the tracker knows this offer at all.
func (q *queueTab) syncTracker(report string, applied bool) (bool, error) {
	apps, err := tracker.LoadApplications(q.l.Tracker())
	if err != nil {
		return false, err
	}
	a, ok := tracker.ByReport(apps, report)
	if !ok {
		return false, nil
	}
	switch {
	case applied && a.Status == tracker.StatusEvaluated:
		return true, tracker.SetStatus(q.l.Tracker(), a.Num, "Applied")
	case !applied && a.Status == tracker.StatusApplied:
		return true, tracker.SetStatus(q.l.Tracker(), a.Num, "Evaluated")
	}
	return true, nil
}

func (q *queueTab) mark(path string, marks state.Marks, id int, what string) tea.Cmd {
	on, err := toggleMark(path, marks, id)
	if err != nil {
		return failf("%v", err)
	}
	if on {
		return notef("#%d marked %s", id, what)
	}
	return notef("#%d unmarked", id)
}

// openResult opens the row's output folder — the tailored CV, its source and the
// job description — in the desktop file manager.
//
// There are three separate reasons a row may not have one, and "no result folder
// yet" covered all three without distinguishing them: a gate that turned the
// score back, a Phase 3 that has not run, and a folder that should be on disk
// and is not. Only the last is a fault, and only the first two are worth telling
// somebody about without alarming them.
func (q *queueTab) openResult(info rowInfo) tea.Cmd {
	if dir, ok := q.cache.ResultDir(q.l.Output(), info.Report); ok {
		return openExternal(dir)
	}
	switch {
	case info.NoCV:
		return failf("#%d made no CV — the score did not clear the gate", info.ID)
	case info.AwaitingCV:
		return failf("#%d has no CV: phase 3 never ran for it", info.ID)
	case info.Kind != kindDone:
		return failf("#%d has no CV yet — it has not been evaluated", info.ID)
	}
	return failf("#%d says it made a CV, but output/ has no folder for it", info.ID)
}

func (q *queueTab) openError(info rowInfo) tea.Cmd {
	r := q.snap.rows[info.ID]
	body := q.cache.ErrorText(q.l.Scores(), q.l.Evals(), q.l.Logs(), info.ID, r)
	return func() tea.Msg {
		return detailMsg{title: fmt.Sprintf("#%d — error", info.ID), body: body}
	}
}

func (q *queueTab) openDebug(info rowInfo) tea.Cmd {
	target, ok := debugTarget(q.l, q.cache, info.ID, q.snap.rows[info.ID])
	if !ok {
		return failf("#%d: nothing landed to debug — the input never arrived", info.ID)
	}
	// The debug target is meant to be *edited* before the retry reads it back,
	// so this one still hands off to the desktop rather than opening read-only.
	return openExternal(target)
}

// ShortHelp and FullHelp answer for whatever is focused right now, which is how
// the footer stays contextual with no hintFor() switch.
func (q *queueTab) ShortHelp() []key.Binding {
	switch {
	case q.review != nil:
		return q.review.help()
	case q.detailOpen:
		return []key.Binding{
			key.NewBinding(key.WithKeys("up/down"), key.WithHelp("↑↓", "scroll")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		}
	// The JD field holding a command IS the palette — it runs on enter and reads
	// as one — so it gets the palette's hints rather than the composer's.
	case q.focus == fCmd, q.focus == fJD && isCommand(q.jd.Value()):
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "run it")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	case q.focus == fFind:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "to the results")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear")),
		}
	case q.focus.typing():
		return []key.Binding{q.keys.Fire, q.keys.Step, q.keys.Out, q.keys.Cycle}
	case q.focus == fAdd:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "add to the queue")), q.keys.Step, q.keys.Cycle}
	case q.focus == fPlay:
		return []key.Binding{q.keys.Start, q.keys.Cycle}
	case q.focus == fList:
		b := []key.Binding{q.keys.Up, q.keys.Down}
		info := q.current()
		if info.stops() > 1 {
			b = append(b, q.keys.PrevAct, q.keys.NextAct)
		}
		switch info.actionAt(q.act) {
		case actProceed:
			b = append(b, q.keys.Yes, q.keys.No)
		default:
			b = append(b, q.keys.Fire)
		}
		return append(b, q.keys.Applied, q.keys.Skip, q.keys.Find)
	}
	return []key.Binding{q.keys.Cycle, q.keys.Down, q.keys.Slash, q.keys.Find}
}

func (q *queueTab) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{q.keys.Cycle, q.keys.Step, q.keys.In, q.keys.Out, q.keys.Fire, q.keys.Slash, q.keys.Find},
		{q.keys.PrevAct, q.keys.NextAct, q.keys.Open, q.keys.Skills},
		{q.keys.Applied, q.keys.Skip, q.keys.Yes, q.keys.No},
	}
}

// statsPaneWidth is the whole left column: 17 columns of content, one of padding
// each side, and the rule that divides the two panes drawn as its right edge.
const statsPaneWidth = 20

// inputBlockHeight is what the composer costs the right column: a three-row
// bordered JD field, the URL line, and a three-row Add button.
const inputBlockHeight = 7

// queueMinW is the narrowest the queue pane can be and still say something: the
// marker and its triage column, the score, a readable company name, and room for
// one action. Below the width where that and a stats pane both fit, the stats
// pane is the one that goes — a lifetime counter is worth less than being able
// to read which company a row is about.
const queueMinW = 4 + 8 + minLabelW + 10

func (q *queueTab) View() string {
	if q.review != nil {
		return q.review.view(q.width, q.height)
	}
	if q.detailOpen {
		return lipgloss.JoinVertical(lipgloss.Left,
			styHeading.Render(truncate(q.detailTitle, q.width)),
			q.detail.View(),
		)
	}

	// The left column is fixed and the right takes the rest, with one column of
	// margin between the rule and the queue.
	rightW := q.width
	stats := ""
	if q.width >= statsPaneWidth+1+queueMinW {
		rightW = q.width - statsPaneWidth - 1
		stats = q.sidePane() + " "
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, stats, q.queuePane(rightW))
}

// sidePane is the left column: what the day is aiming for at the top, what the
// pipeline is doing at the bottom.
//
// The two are pinned apart rather than stacked because they are read on
// different clocks. Goals move a few times a day and are the thing you look up
// at; the counters move every second and sit level with the composer, which is
// where the eye already is when a run is fired.
func (q *queueTab) sidePane() string {
	const inner = statsPaneWidth - 3 // one border, two padding

	stats := q.statsBlock(inner)
	room := q.height - lipgloss.Height(stats)

	// The goals give way when the column is short — a target is worth less than
	// knowing what is running — but they give way a whole group at a time.
	// Clipped by the line, what survived at 18 rows was the heading and nothing
	// underneath it.
	var goals []string
	for _, g := range q.goalGroups(inner) {
		if len(goals)+len(g) > room {
			break
		}
		goals = append(goals, g...)
	}
	if len(goals) < minGoalsH {
		goals = nil
	}

	lines := goals
	for range room - len(goals) {
		lines = append(lines, "")
	}
	return styRule.Width(statsPaneWidth).Height(q.height).MaxHeight(q.height).
		Render(strings.Join(append(lines, stats), "\n"))
}

// minGoalsH is the heading, its blank, and one goal with its bar. Below that the
// block says less than the space it costs.
const minGoalsH = 4

// goalGroups is the day measured against its targets — today's scans and
// applications, the week's applications, the streak underneath — as the units
// the pane drops it in.
//
// The counts come from the same buckets the activity tab draws, asked of the
// cache rather than carried in the snapshot: it re-reads only when the evals
// directory or applied.tsv has actually moved, and the activity tab has been
// reading them this way all along.
func (q *queueTab) goalGroups(inner int) [][]string {
	act := q.cache.Activity(q.l.Evals(), q.l.Applied())
	now := time.Now()
	today := state.DayKey(now)

	goal := func(label string, n, target int) []string {
		if target <= 0 {
			// An unset target is not a goal, so it gets a count and no bar.
			return []string{spread(styDim.Render(label), strconv.Itoa(n), inner)}
		}
		return []string{
			spread(styDim.Render(label), fmt.Sprintf("%d/%d", n, target), inner),
			bar(n, target, inner),
		}
	}
	return [][]string{
		{styPaneHead.Render("Goals"), ""},
		goal("scans today", act.Scans.Days[today], q.goals.DailyScans),
		goal("apps today", act.Apps.Days[today], q.goals.DailyApplied),
		goal("apps week", weekTotal(act.Apps, now), q.goals.WeeklyApplied),
		{"", spread(styDim.Render("streak"), fmt.Sprintf("%dd", act.Scans.Streak(now)), inner)},
	}
}

// weekTotal sums a bucket from Monday to today. SumPrefix cannot answer this —
// its keys are calendar prefixes, and a week straddles two months four times a
// year.
func weekTotal(b state.Buckets, now time.Time) int {
	n := 0
	for d := mondayOf(now); !d.After(now); d = d.AddDate(0, 0, 1) {
		n += b.Days[state.DayKey(d)]
	}
	return n
}

// bar draws n against target as a filled rule, banded the way the score column
// is: green once the target is met, gold from halfway, dim while it is still
// mostly ahead.
//
// A count above zero always lights at least one cell. Rounding one scan out of
// ten down to an empty bar reads as "nothing yet", which is the opposite of the
// thing worth encouraging.
func bar(n, target, w int) string {
	if target <= 0 || w <= 0 {
		return ""
	}
	filled := min(w, n*w/target)
	if n > 0 && filled == 0 {
		filled = 1
	}
	sty := styDim
	switch {
	case n >= target:
		sty = styOK
	case n*2 >= target:
		sty = styAccent
	}
	return sty.Render(strings.Repeat("▓", filled)) +
		styDim.Render(strings.Repeat("░", w-filled))
}

// statsBlock is the live counters over the lifetime totals.
func (q *queueTab) statsBlock(inner int) string {
	s := q.snap.stats

	row := func(label, value string, indent bool, val lipgloss.Style) string {
		l, v := label, val.Render(value)
		if indent {
			l = styDim.Render(" └ " + label)
		} else {
			v = val.Bold(true).Render(value)
		}
		return spread(l, v, inner)
	}
	num := func(n int) string { return strconv.Itoa(n) }

	lines := []string{
		styPaneHead.Render("Stats"),
		"",
		row("Queue", num(len(q.snap.queue)), false, lipgloss.NewStyle()),
		row("Active", num(boolInt(q.snap.running)), false, lipgloss.NewStyle()),
		row("Paused", num(len(q.snap.paused)), false, dueStyle(len(q.snap.paused))),
		row("Failed", num(s.Failed), false, failedStyle(s.Failed)),
		row("Completed", num(s.Completed), false, lipgloss.NewStyle()),
		row("CV", num(s.CV), true, styDim),
		row("AVG", dashOr(q.snap.avgOK, fmt.Sprintf("%.1f", q.snap.avg)), true, styDim),
		row("CV rate", ratePct(s), true, styDim),
		row("P1-gated", num(s.Gated), false, lipgloss.NewStyle()),
		"",
		row("Follow-ups", num(q.dueCount()), false, dueStyle(q.dueCount())),
	}
	return strings.Join(lines, "\n")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (q *queueTab) dueCount() int {
	if q.due == nil {
		return 0
	}
	return q.due()
}

func dueStyle(n int) lipgloss.Style {
	if n > 0 {
		return styAccent
	}
	return lipgloss.NewStyle()
}

func failedStyle(n int) lipgloss.Style {
	if n > 0 {
		return styErr
	}
	return lipgloss.NewStyle()
}

func dashOr(ok bool, s string) string {
	if ok {
		return s
	}
	return "—"
}

func ratePct(s state.Stats) string {
	r, ok := s.Rate()
	if !ok {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", r*100)
}

// queuePane is the heading, the windowed list, and the composer pinned to the
// bottom of the column.
//
// The composer sits at the BOTTOM, under the rows it adds to, and the list is
// anchored to the bottom with it: a row lands directly above the field it was
// pasted into rather than a screen away, and the gap a short list leaves opens
// overhead instead of between the two things the eye is moving between.
func (q *queueTab) queuePane(w int) string {
	input := q.inputBlock(w)

	// Two rows for the heading and its blank, the rest to the list — minus
	// whatever the composer actually rendered, which shrinks when the pane is
	// too narrow for a bordered field.
	q.listWindow = q.height - 2 - lipgloss.Height(input)
	if q.listWindow < 1 {
		q.listWindow = 1
	}
	list := lipgloss.NewStyle().Height(q.listWindow).MaxHeight(q.listWindow).
		AlignVertical(lipgloss.Bottom).Render(q.listView(w))

	head := styPaneHead.Render("Queue")
	if q.busy != "" {
		// A command runs in its own goroutine and can take a minute. The
		// footer's note is swept after five seconds, so it cannot be what says
		// this is still going.
		head += "  " + styAccent.Render(q.busy)
	}
	// The find line takes the blank under the heading rather than a row of its
	// own: it has to stay visible after Enter hands the keyboard to the list, and
	// a list that changed height the moment you started typing would be a list
	// jumping under the cursor.
	return lipgloss.NewStyle().Width(w).MaxWidth(w).Render(
		lipgloss.JoinVertical(lipgloss.Left, head, q.findLine(w), list, input))
}

// findLine is the filter, shown whenever it is doing something — being typed
// into, or holding rows back.
func (q *queueTab) findLine(w int) string {
	if q.focus != fFind && q.find.Value() == "" {
		return ""
	}
	line := styDim.Render("find ") + q.find.Value()
	if q.focus == fFind {
		line += styAccent.Render("▏")
	}
	if q.find.Value() != "" {
		line += styDim.Render(fmt.Sprintf("  %d of %d", len(q.visible), len(q.snap.session)))
	}
	return truncate(line, w)
}

// inputBlock is the composer: the JD field with the run button beside it, the
// optional URL under them, and the Add button.
//
// The run button is next to the JD field rather than on its own line because
// adding and running are the two halves of one motion — paste, then go — and
// separating them made "run" read as a property of the queue rather than an
// action.
func (q *queueTab) inputBlock(w int) string {
	// Play is 5 columns of border, padding and glyph; one more separates them.
	jdW := w - 6
	if jdW < 12 {
		// No room for the bordered pair. The fields still work; they just render
		// as plain lines, which is what the frame can actually hold.
		return q.plainInput(w)
	}

	jd := styField(q.focus == fJD || q.focus == fCmd).Width(jdW).MaxWidth(jdW).Render(q.jdText(jdW - 4))

	glyph := styOK.Bold(true).Render("▶")
	if q.snap.running {
		glyph = styAccent.Render(spinnerFrames[q.frame%len(spinnerFrames)])
	}
	play := styField(q.focus == fPlay).Render(glyph)

	// The URL gets one line, whatever is in it. A pasted LinkedIn link is 400
	// columns of tracking parameters, and the Width() this pane is rendered at
	// WRAPS rather than cuts — so an untruncated one added five rows to a pane
	// whose height was already spent, and MaxHeight clipped the overflow off the
	// bottom. What it clipped was the Add button and the frame's own border.
	//
	// The head is what survives: the domain and the job id say which posting
	// this is, and everything past `&` is what the site appends for itself.
	const urlLabel = " URL (optional): "
	room := max(4, w-lipgloss.Width(urlLabel)-1) // -1 leaves the caret its column

	urlLine := urlLabel // not `url`: net/url is imported, and shadowing it here reads as a bug
	switch {
	case q.url.Value() != "":
		urlLine += truncate(q.url.Value(), room)
	case q.focus == fURL:
	default:
		urlLine += styDim.Render("—")
	}
	if q.focus == fURL {
		urlLine += styAccent.Render("▏")
	}

	add := "Add to queue"
	if q.focus == fAdd {
		add = styAccent.Bold(true).Render(add)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Top, jd, " ", play),
		urlLine,
		styField(q.focus == fAdd).Render(add))
}

// jdText is what the JD field shows: the character count and the first line once
// something is pasted, the placeholder before that.
//
// The cursor sits BEFORE the placeholder rather than after it — a placeholder is
// a hint, not text you are editing, and a caret at its end reads as though it
// were.
func (q *queueTab) jdText(w int) string {
	// The palette borrows this box rather than taking a row of its own: it is
	// the field the eye is already on, it is already bordered and already lights
	// up when focused, and the composer keeps its height so the list does not
	// jump. What it holds comes back on esc.
	if q.focus == fCmd {
		return truncate(styAccent.Render("/")+q.cmd.Value()+styAccent.Render("▏"), w)
	}
	caret := ""
	if q.focus == fJD {
		caret = styAccent.Render("▏")
	}
	v := q.jd.Value()
	if v == "" {
		return truncate(caret+styDim.Render(q.jd.Placeholder), w)
	}
	// A line that starts with a slash is a command, not a paste: Enter already
	// runs it as one, so it reads as the palette does rather than being counted
	// off in characters.
	if isCommand(v) {
		s := strings.TrimSpace(v)
		return truncate(styAccent.Render("/")+s[1:]+caret, w)
	}
	first, _, _ := strings.Cut(v, "\n")
	return truncate(fmt.Sprintf("%s chars — %s", thousands(len(v)), first), w-1) + caret
}

// thousands groups a character count, so a 12,000-character paste reads as one
// number rather than five digits to be counted off.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// plainInput is the narrow fallback: the same three fields with no borders.
func (q *queueTab) plainInput(w int) string {
	mark := func(f qFocus) string {
		if q.focus == f {
			return styAccent.Render("▏")
		}
		return " "
	}
	add := styDim.Render(" [ add ]")
	if q.focus == fAdd {
		add = styFocused.Render(" add ")
	}
	first := mark(fJD) + q.jd.View()
	if q.focus == fCmd {
		first = styAccent.Render("▏/") + q.cmd.View()
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		first,
		mark(fURL)+q.url.View(),
		add)
}

// listView renders the session as a log grouped by day: a dated heading above
// each run of rows sharing one, filled from the BOTTOM so the newest rows sit on
// the composer and the gap a short list leaves opens above them.
//
// The list was already chronological and already followed its own tail, and the
// composer was already pinned under it — so the void a half-full queue left was
// simply on the wrong side of the rows.
//
// The window counts LINES, not rows, because a heading costs a line and how many
// headings a window holds depends on where it starts. Filling upward from a
// known end is what makes that answerable in one pass; see backfill.
//
// The indicator rows are reserved, not conditional: a list that grows past the
// window would otherwise shift every row up by one at the moment the arrow
// appears, which reads as the list jumping under the cursor. Blank they cost
// nothing here — the top one is absorbed by the gap above, and the bottom one
// keeps the newest row off the composer.
//
// Scroll is derived here rather than nudged by the movement keys. There is one
// expression that decides which slice is visible, and it runs on every frame, so
// a cursor moved by a key and a cursor moved by a poll cannot disagree.
func (q *queueTab) listView(w int) string {
	n := len(q.visible)
	if n == 0 {
		if q.find.Value() != "" {
			return styDim.Render(truncate("Nothing matches — esc clears the filter.", w))
		}
		// Bounded like every row below it. At 31 columns this outlasts a narrow
		// pane, and the Width() the pane is rendered at wraps rather than cuts —
		// one wrapped line here costs the composer a row off the bottom.
		return styDim.Render(truncate("Nothing yet — paste a JD below.", w))
	}
	budget := max(1, q.listWindow-2) // the two indicator rows
	// Below two lines a heading would cost the only row its line, and a dated
	// rule over nothing is worse than an undated row.
	headed := budget >= 2

	end := q.scroll
	if q.focus != fList || end < 1 || end > n {
		// Not navigating — follow the latest additions, which is where a run
		// puts the rows worth watching.
		end = n
	}
	start := q.backfill(end, budget)
	// Hold the cursor inside the window, but only while it means something.
	// Unfocused it rests at 0, and honouring it there dragged the window to the
	// oldest row in history on every frame — the tail-following above undone by
	// the line meant to protect navigation.
	if q.focus == fList {
		if q.cursor >= end {
			end = q.cursor + 1
			start = q.backfill(end, budget)
		}
		// Walked off the top. Navigation moves one row per keypress, so this is
		// one turn of the loop; a cursor clamped after the session shrank is the
		// only case that costs more, and it is bounded by n.
		for q.cursor < start && end > q.cursor+1 {
			end--
			start = q.backfill(end, budget)
		}
	}
	q.scroll = end

	cells := make([]rowCells, 0, end-start)
	for i := start; i < end; i++ {
		cells = append(cells, q.rowCells(describe(q.visible[i], q.snap), q.focus == fList && i == q.cursor))
	}

	lines := []string{indicator("↑", start)}
	for i, row := range layoutRows(cells, w, q.focus == fList) {
		if at := start + i; headed && (at == start || q.day(at) != q.day(at-1)) {
			// The topmost group shows its date even when the window cut into
			// the middle of it, so no visible row is ever undated.
			lines = append(lines, daySep(q.day(at), w))
		}
		lines = append(lines, row)
	}
	return strings.Join(append(lines, indicator("↓", n-end)), "\n")
}

// backfill walks UP from end (exclusive) and returns the first row index whose
// group still fits in budget lines, headings counted.
//
// Upward is the direction this can be answered in one pass. Adding a row above
// one of the same day is free — the heading that row already carries just moves
// up to it — while a row from an earlier day brings its own. Downward, every row
// added can retroactively change whether the row above it needed a heading.
func (q *queueTab) backfill(end, budget int) int {
	start, lines := end, 0
	for start > 0 {
		cost := 1
		if lines == 0 || q.day(start) != q.day(start-1) {
			cost++ // the topmost group's date, or a new group above this one
		}
		if lines+cost > budget {
			break
		}
		lines += cost
		start--
	}
	if start == end && end > 0 {
		// Not even one row and its heading fit. Draw the row anyway and let the
		// pane's MaxHeight take what is over — an empty list under a queue with
		// rows in it reads as "nothing to do".
		start = end - 1
	}
	return start
}

// day is the calendar day row i is filed under, as a sortable key.
//
// A row the scan could not date has nothing on disk yet, which means it was
// queued moments ago: today is not a guess there, it is the answer.
func (q *queueTab) day(i int) string {
	t, ok := q.snap.days[q.visible[i]]
	if !ok || t.IsZero() {
		t = time.Now()
	}
	return state.DayKey(t)
}

// daySep is the dated rule that opens a day's run of rows.
func daySep(key string, w int) string {
	label := key // unreachable: the key came from Format with this same layout
	if t, err := time.Parse("2006-01-02", key); err == nil {
		label = dayLabel(t)
	}
	head := "── " + label + " "
	if n := w - lipgloss.Width(head); n > 0 {
		head += strings.Repeat("─", n)
	}
	return styDim.Render(truncate(head, w))
}

// dayLabel names a day the way you would say it out loud. The two most recent
// get words, because on a list you added to an hour ago that is what "when"
// means; everything older gets a date, and a date from another year gets its
// year with it.
func dayLabel(t time.Time) string {
	now := time.Now()
	switch state.DayKey(t) {
	case state.DayKey(now):
		return "today"
	case state.DayKey(now.AddDate(0, 0, -1)):
		return "yesterday"
	}
	if t.Year() == now.Year() {
		return fmt.Sprintf("%s %d %s", t.Format("Mon"), t.Day(), monthNames[int(t.Month())-1])
	}
	return fmt.Sprintf("%d %s %d", t.Day(), monthNames[int(t.Month())-1], t.Year())
}

func indicator(arrow string, n int) string {
	if n <= 0 {
		return " "
	}
	return styDim.Render(fmt.Sprintf("%s %d more", arrow, n))
}

// rowCells is one row broken into its columns, unpadded. Splitting the render
// from the layout is what lets the list align: the widths cannot be known until
// every visible row has been built.
// rowCells is one row broken into its columns, unpadded.
//
// actions and routine are separate because they are droppable to different
// degrees. actions is what the row is ASKING for — a gate to overrule, a failure
// to look at — and it goes last. routine is "report | link", which is the same
// on almost every row and reachable with → and o, so it is the first thing to
// give way when the label has nowhere left to shrink.
type rowCells struct {
	marker, mark, label, score, actions, routine string
	focused                                      bool
}

func (q *queueTab) rowCells(info rowInfo, focused bool) rowCells {
	c := rowCells{marker: info.marker(), mark: info.mark(), score: info.scoreText()}
	// The glyph spins only while a run actually holds the lock. Without that
	// test it animates on rows nobody is working on — a batch left paused, a
	// Phase 3 that never ran — which is the one thing a spinner must not say.
	//
	// ponytail: every open row spins, not only the one stage in flight. Which
	// offer that is lives in the checkpoints, and the snapshot deliberately does
	// not read them while a run is active. In the case that matters — a run
	// working the rows it started — they are the same set.
	if info.inFlight() && q.snap.running {
		c.marker = styAccent.Render(spinnerFrames[q.frame%len(spinnerFrames)])
	}
	if info.Applied {
		c.mark = styAccent.Render(c.mark)
	} else {
		c.mark = styDim.Render(c.mark)
	}
	c.label = info.Label
	if focused && q.act == 0 {
		c.label = styFocused.Render(c.label)
	}

	c.focused = focused
	var must, routine []string
	for i, a := range info.Actions {
		// Both halves, together: the action index is per-LIST, not per-row, so
		// `q.act == i+1` is true of one slot on every row at once. Asking it
		// alone inverted the same action on all twenty.
		text := q.actionText(a, info, focused, focused && q.act == i+1)
		if asks(a.id) {
			must = append(must, text)
		} else {
			routine = append(routine, text)
		}
	}
	c.actions = strings.Join(must, styDim.Render(" | "))
	c.routine = strings.Join(routine, styDim.Render(" | "))
	return c
}

// asks reports whether an action is the row putting a question to the reader,
// as opposed to an offer that is on almost every row anyway.
func asks(id actionID) bool {
	switch id {
	case actProceed, actRetry, actDebug, actError:
		return true
	}
	return false
}

// actionText decides how loudly an action asks to be pressed.
//
// Twenty rows each ending "report | link" in yellow and blue is the loudest
// thing on the page and the least informative — the same two words, on almost
// every row, competing with the score for the eye. They are dim until the row is
// focused.
//
// What stays loud is what the row is ASKING for: a gate to overrule, a failure
// to look at, a retry to fire. Those are not routine, they do not appear on most
// rows, and a failed row that does not advertise a way out is worse than a busy
// one.
func (q *queueTab) actionText(a action, info rowInfo, rowFocused, isFocus bool) string {
	if isFocus {
		return styFocused.Render(a.label)
	}
	var out string
	switch a.id {
	case actError:
		out = styErr.Underline(true).Render(a.label)
	case actProceed, actRetry, actDebug:
		out = styAction.Render(a.label)
	case actLink:
		if rowFocused {
			out = styLink.Render(a.label)
		} else {
			out = styDim.Underline(true).Render(a.label)
		}
	default:
		if rowFocused {
			out = styAction.Render(a.label)
		} else {
			out = styDim.Render(a.label)
		}
	}
	if a.id == actLink {
		out = osc8(out, info.Link)
	}
	return out
}

// layoutRows pads the cells into columns.
//
// The score and the right-hand group are measured across the whole window and
// every row is padded to the widest, so both land in the same column on every
// line. Ragged fields are the whole of what makes a list of twenty rows read as
// noise: the eye has to re-find the score on each one instead of running down a
// column.
//
// Widths come from the rows on screen rather than a constant, so a window with
// no failures does not reserve twenty-five columns for "see error | retry |
// debug".
// minLabelW is where a company name stops being identifiable. Below this the
// row is a truncation with some numbers after it, and the numbers are not what
// anyone is looking for.
const minLabelW = 30

// layoutRows pads the cells into columns.
//
// The score and the right-hand columns are measured across the whole window and
// every row padded to the widest, so each lands in the same place on every line.
// Ragged fields are the whole of what makes twenty rows read as noise: the eye
// has to re-find the score on each one instead of running down a column.
//
// Widths come from the rows on screen rather than constants, so a window with no
// failures does not reserve twenty-five columns for "see error | retry | debug",
// and a column nothing uses takes no space at all.
//
// When even that does not fit, columns are dropped in order of what the reader
// loses: the routine actions first (the same two words on every row, and one
// keystroke away), then the status. The label and the score never go.
func layoutRows(cells []rowCells, w int, listFocused bool) []string {
	widest := func(get func(rowCells) string) int {
		n := 0
		for _, c := range cells {
			n = max(n, lipgloss.Width(get(c)))
		}
		return n
	}
	// A column nothing uses costs nothing: its gap goes with it.
	gap := func(n int) int {
		if n == 0 {
			return 0
		}
		return n + 2
	}

	scoreW := widest(func(c rowCells) string { return c.score })
	asksW := widest(func(c rowCells) string { return c.actions })
	fullW := widest(func(c rowCells) string { return joinActions(c.actions, c.routine) })

	label := func(rightW int) int { return w - 4 - gap(scoreW) - gap(rightW) }

	rightW, routineShown := fullW, true
	if label(rightW) < minLabelW {
		routineShown = false
		// The focused row keeps its routine actions whatever the width — they
		// are what → is about to walk, and a stop you cannot see is worse than a
		// short label. So the column still reserves room for them while the list
		// has focus, which also stops it resizing as the cursor moves between a
		// row offering "proceed? | link" and one offering "report | link".
		rightW = asksW
		if listFocused {
			rightW = max(asksW, fullW)
		}
	}
	labelW := max(12, label(rightW))

	cell := func(s string, width int, right bool) string {
		if width == 0 {
			return ""
		}
		if right {
			return "  " + lpad(s, width)
		}
		return "  " + pad(s, width)
	}

	out := make([]string, 0, len(cells))
	for _, c := range cells {
		acts := c.actions
		if routineShown || c.focused {
			acts = joinActions(c.actions, c.routine)
		}
		// The actions are right-aligned. "link" is the most repeated token on
		// the page and it ends every row that has one, so aligning its right
		// edge draws the strongest vertical line available — where aligning the
		// left edge left "proceed? | link" hanging two columns past the rows
		// around it, which is what a half-width window made obvious.
		out = append(out, strings.TrimRight(c.marker+c.mark+" "+
			pad(labelText(c.label, labelW), labelW)+
			cell(c.score, scoreW, true)+
			cell(acts, rightW, true), " "))
	}
	return out
}

func joinActions(must, routine string) string {
	switch {
	case must == "":
		return routine
	case routine == "":
		return must
	}
	return must + styDim.Render(" | ") + routine
}

// labelText truncates to the column and dims the role.
//
// "Company — Role" is one string doing two jobs. The company is what a reader
// scans for and the role is context, so the role is dimmed and the eye lands on
// the left of each line instead of reading all of it. Truncation happens on the
// RAW text and the pieces are styled after, because a style sequence measured as
// characters blows the column apart.
func labelText(raw string, w int) string {
	if strings.Contains(raw, "\x1b") { // already styled: the focused row
		return truncate(raw, w)
	}
	t := truncate(raw, w)
	if i := strings.Index(t, " — "); i > 0 {
		return t[:i] + styDim.Render(t[i:])
	}
	return t
}

func (q *queueTab) Title() string  { return "queue" }
func (q *queueTab) Badge() int     { return 0 }
func (q *queueTab) setFrame(n int) { q.frame = n }
