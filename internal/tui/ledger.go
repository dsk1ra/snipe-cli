package tui

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/paths"
)

// ledgerScreen is the review of the fact ledger (docs/PRD-TAILORING-REWORK.md
// §6.1): every claim cv.md makes, one at a time, with its bullet around it. The
// ledger is what Phase 3 may say, so a fact nobody approved is a fact it cannot
// use — and ~140 of them are only reviewable if each costs one keypress.
//
// Every decision is written before the next key is read. The file holds
// approvals that nothing else records, and a rejection must outlive a rebuild or
// the model proposes the same claim again.
type ledgerScreen struct {
	path string
	led  *ledger.Ledger
	pos  []ledgerPos // facts then framings of each bullet, in file order
	cur  int

	undo   []ledgerUndo
	edited map[string]bool // fact ids changed this session; the file cannot say

	edit *textinput.Model // non-nil while the fact text is being retyped
	del  bool             // d was pressed; the next digit removes that entity/figure
}

// ledgerPos addresses one reviewable item. Indexes rather than pointers: Save
// marshals the whole tree, and an undo restores in place.
type ledgerPos struct {
	e, b, i int
	framing bool
}

// ledgerUndo is the item as it was before a change. Decisions, edits and list
// removals all go through the same stack, so u takes back whichever came last.
type ledgerUndo struct {
	p      ledgerPos
	f      ledger.Fact
	fr     ledger.Framing
	edited bool
}

type ledgerMsg struct {
	s   *ledgerScreen
	err error
}

func openLedger(l paths.Layout) tea.Cmd {
	return func() tea.Msg {
		led, err := ledger.Load(l.CVLedger())
		if err != nil {
			return ledgerMsg{err: err}
		}
		s := newLedgerScreen(l.CVLedger(), led)
		if len(s.pos) == 0 {
			return ledgerMsg{err: errors.New("the fact ledger is empty: `snipe ledger` builds it from cv.md")}
		}
		return ledgerMsg{s: s}
	}
}

func newLedgerScreen(path string, led *ledger.Ledger) *ledgerScreen {
	s := &ledgerScreen{path: path, led: led, edited: map[string]bool{}}
	for e, en := range led.Entries {
		for b, bl := range en.Bullets {
			for i := range bl.Facts {
				s.pos = append(s.pos, ledgerPos{e: e, b: b, i: i})
			}
			for i := range bl.Framings {
				s.pos = append(s.pos, ledgerPos{e: e, b: b, i: i, framing: true})
			}
		}
	}
	// Start on the first undecided item: reopening mid-review resumes.
	if i := s.find(-1, 1, s.pending); i >= 0 {
		s.cur = i
	}
	return s
}

var ledgerKeys = struct {
	Approve, Reject, Edit, MustKeep, Drop, Undo, Pending, Flagged, Move, Close key.Binding
}{
	Approve: key.NewBinding(key.WithKeys("a", "enter"), key.WithHelp("a", "approve")),
	Reject:  key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "reject")),
	Edit:    key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
	// m, not k: k is "up" in every other list, and a key that moves in one
	// screen and flips a flag in the next costs a fact.
	MustKeep: key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "must keep")),
	Drop:     key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "drop an entity/figure")),
	Undo:     key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "undo")),
	Pending:  key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "next/prev pending")),
	Flagged:  key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "next rejected/edited")),
	Move:     key.NewBinding(key.WithKeys("up", "down", "j", "k"), key.WithHelp("↑↓", "move")),
	Close:    key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "close")),
}

func (s *ledgerScreen) help() []key.Binding {
	if s.edit != nil {
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save and approve")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	}
	return []key.Binding{ledgerKeys.Approve, ledgerKeys.Reject, ledgerKeys.Edit, ledgerKeys.MustKeep,
		ledgerKeys.Drop, ledgerKeys.Undo, ledgerKeys.Pending, ledgerKeys.Flagged, ledgerKeys.Move, ledgerKeys.Close}
}

// capturing is whether the screen is mid-gesture, when esc and q belong to it
// rather than closing it: a q typed into a fact is a q.
func (s *ledgerScreen) capturing() bool { return s.edit != nil || s.del }

func (s *ledgerScreen) fact(p ledgerPos) *ledger.Fact {
	return &s.led.Entries[p.e].Bullets[p.b].Facts[p.i]
}

func (s *ledgerScreen) framing(p ledgerPos) *ledger.Framing {
	return &s.led.Entries[p.e].Bullets[p.b].Framings[p.i]
}

func (s *ledgerScreen) pending(p ledgerPos) bool {
	if p.framing {
		fr := s.framing(p)
		return !fr.Approved && !fr.Rejected
	}
	return s.fact(p).Status == ""
}

func (s *ledgerScreen) flagged(p ledgerPos) bool {
	if p.framing {
		return s.framing(p).Rejected
	}
	f := s.fact(p)
	return f.Status == "rejected" || s.edited[f.ID]
}

// find is the first item satisfying ok, walking from the one after from in
// direction dir and wrapping once. -1 if none does.
func (s *ledgerScreen) find(from, dir int, ok func(ledgerPos) bool) int {
	n := len(s.pos)
	for k := 1; k <= n; k++ {
		i := ((from+dir*k)%n + n) % n
		if ok(s.pos[i]) {
			return i
		}
	}
	return -1
}

// progress is facts decided over facts in all, and the framings still to decide.
func (s *ledgerScreen) progress() (done, facts, rejected, framings int) {
	for _, p := range s.pos {
		switch {
		case p.framing:
			if s.pending(p) {
				framings++
			}
		default:
			facts++
			if !s.pending(p) {
				done++
			}
			if s.fact(p).Status == "rejected" {
				rejected++
			}
		}
	}
	return
}

func (s *ledgerScreen) key(k tea.KeyPressMsg) tea.Cmd {
	if len(s.pos) == 0 {
		return nil
	}
	if s.edit != nil {
		return s.editKey(k)
	}
	if s.del {
		s.del = false
		if ks := k.String(); len(ks) == 1 && ks[0] >= '1' && ks[0] <= '9' {
			return s.drop(int(ks[0] - '1'))
		}
		return nil
	}
	p := s.pos[s.cur]
	switch {
	case key.Matches(k, ledgerKeys.Move):
		d := 1
		if ks := k.String(); ks == "up" || ks == "k" {
			d = -1
		}
		s.cur = max(0, min(len(s.pos)-1, s.cur+d))
	case key.Matches(k, ledgerKeys.Pending):
		d := 1
		if k.String() == "shift+tab" {
			d = -1
		}
		if i := s.find(s.cur, d, s.pending); i >= 0 {
			s.cur = i
		} else {
			return notef("nothing left to review")
		}
	case key.Matches(k, ledgerKeys.Flagged):
		if i := s.find(s.cur, 1, s.flagged); i >= 0 {
			s.cur = i
		} else {
			return notef("nothing rejected or edited")
		}
	case key.Matches(k, ledgerKeys.Approve):
		return s.decide(p, true)
	case key.Matches(k, ledgerKeys.Reject):
		return s.decide(p, false)
	case key.Matches(k, ledgerKeys.Undo):
		return s.back()
	case p.framing:
		return nil // the rest edit a fact's text and lists
	case key.Matches(k, ledgerKeys.Edit):
		in := textinput.New()
		in.Prompt = "edit  "
		in.SetWidth(200) // the screen wraps nothing here; the field scrolls
		in.SetValue(s.fact(p).Text)
		in.CursorEnd()
		in.Focus()
		s.edit = &in
	case key.Matches(k, ledgerKeys.MustKeep):
		s.push(p)
		f := s.fact(p)
		f.MustKeep = !f.MustKeep
		return s.save()
	case key.Matches(k, ledgerKeys.Drop):
		if f := s.fact(p); len(f.Entities)+len(f.Figures) > 0 {
			s.del = true
		}
	}
	return nil
}

func (s *ledgerScreen) editKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		s.edit = nil
		return nil
	case "enter":
		text := strings.TrimSpace(s.edit.Value())
		s.edit = nil
		p := s.pos[s.cur]
		if text == "" {
			return failf("a fact cannot be empty; x rejects it")
		}
		if text != s.fact(p).Text {
			s.push(p)
			s.fact(p).Text = text
			s.edited[s.fact(p).ID] = true
			return s.decideNoUndo(p, true)
		}
		return s.decide(p, true)
	}
	// The blink command is dropped: the cursor is drawn steady, and a command
	// that sleeps is one more thing a test or a quit has to wait out.
	*s.edit, _ = s.edit.Update(k)
	return nil
}

// push remembers p as it is now. The slices are cloned: removing an entity
// edits them in place, and an undo that restored the shared array restored
// nothing.
func (s *ledgerScreen) push(p ledgerPos) {
	u := ledgerUndo{p: p}
	if p.framing {
		u.fr = *s.framing(p)
		u.fr.From = slices.Clone(u.fr.From)
	} else {
		u.f = *s.fact(p)
		u.f.Entities = slices.Clone(u.f.Entities)
		u.f.Figures = slices.Clone(u.f.Figures)
		u.edited = s.edited[u.f.ID]
	}
	s.undo = append(s.undo, u)
}

// decide approves or rejects the item and moves to the next undecided one.
func (s *ledgerScreen) decide(p ledgerPos, approve bool) tea.Cmd {
	s.push(p)
	return s.decideNoUndo(p, approve)
}

// decideNoUndo is for an edit, which has pushed its own snapshot already: one
// keypress, one undo.
func (s *ledgerScreen) decideNoUndo(p ledgerPos, approve bool) tea.Cmd {
	if p.framing {
		fr := s.framing(p)
		fr.Approved, fr.Rejected = approve, !approve
	} else {
		st := "rejected"
		if approve {
			st = "approved"
		}
		s.fact(p).Status = st
	}
	if i := s.find(s.cur, 1, s.pending); i >= 0 {
		s.cur = i
	}
	return s.save()
}

// back takes the last change back and puts the cursor on it, so the item can be
// decided again straight away.
func (s *ledgerScreen) back() tea.Cmd {
	if len(s.undo) == 0 {
		return notef("nothing to undo")
	}
	u := s.undo[len(s.undo)-1]
	s.undo = s.undo[:len(s.undo)-1]
	if u.p.framing {
		*s.framing(u.p) = u.fr
	} else {
		*s.fact(u.p) = u.f
		if u.edited {
			s.edited[u.f.ID] = true
		} else {
			delete(s.edited, u.f.ID)
		}
	}
	if i := slices.Index(s.pos, u.p); i >= 0 {
		s.cur = i
	}
	return s.save()
}

// drop removes the nth entry of the fact's entities then figures, numbered as
// the detail shows them. The model's lists are noisy, and a wrong entity is
// worse than a missing one: the verifier treats it as something the CV claims.
func (s *ledgerScreen) drop(n int) tea.Cmd {
	p := s.pos[s.cur]
	f := s.fact(p)
	if n >= len(f.Entities)+len(f.Figures) {
		return nil
	}
	s.push(p)
	if n < len(f.Entities) {
		f.Entities = slices.Delete(f.Entities, n, n+1)
	} else {
		f.Figures = slices.Delete(f.Figures, n-len(f.Entities), n-len(f.Entities)+1)
	}
	s.edited[f.ID] = true
	return s.save()
}

func (s *ledgerScreen) save() tea.Cmd {
	if err := ledger.Save(s.path, s.led); err != nil {
		return failf("the ledger was not saved: %v", err)
	}
	return nil
}

// --- view ---

var styMark = lipgloss.NewStyle().Foreground(colAccent).Bold(true).Underline(true)

func (s *ledgerScreen) view(w, h int) string {
	done, facts, rejected, framings := s.progress()
	status := fmt.Sprintf("Fact ledger · %d/%d reviewed", done, facts)
	if rejected > 0 {
		status += fmt.Sprintf(" · %d rejected", rejected)
	}
	if framings > 0 {
		status += fmt.Sprintf(" · %d framing(s) to decide", framings)
	}
	if done == facts && framings == 0 {
		status += " — all reviewed"
	}
	p := s.pos[s.cur]
	en := s.led.Entries[p.e]
	bl := en.Bullets[p.b]
	where := fmt.Sprintf("%s · entry %d/%d · bullet %d/%d", en.Title, p.e+1, len(s.led.Entries), p.b+1, len(en.Bullets))
	if bl.Render != nil && !*bl.Render {
		where += " · not printed on the CV"
	}
	out := []string{styHeading.Render(truncate(status, w)), styDim.Render(truncate(where, w)), ""}

	detail := s.detail(w)
	tw := max(10, w-2)
	body := strings.Split(wrapText(s.bulletText(p, bl.Text), tw), "\n")
	// Leave the list and the detail their room before the bullet takes more.
	body = body[:min(len(body), max(3, h/3))]
	out = append(out, body...)
	out = append(out, "")

	list := s.list(p, w)
	listH := max(3, h-len(out)-len(detail)-1)
	if len(list.lines) > listH {
		start := max(0, min(list.at-listH/2, len(list.lines)-listH))
		list.lines = list.lines[start : start+listH]
	}
	out = append(out, list.lines...)
	out = append(out, styDim.Render(strings.Repeat("─", max(0, w))))
	return strings.Join(append(out, detail...), "\n")
}

// bulletText is the source bullet with the focused fact marked where it sits in
// it. Facts are cut from the bullet but not always verbatim, so one that does
// not match is not marked; the detail line below still shows it whole.
func (s *ledgerScreen) bulletText(p ledgerPos, text string) string {
	if p.framing {
		return text
	}
	loc := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(s.fact(p).Text)).FindStringIndex(text)
	if loc == nil {
		return text
	}
	return text[:loc[0]] + styMark.Render(text[loc[0]:loc[1]]) + text[loc[1]:]
}

type ledgerList struct {
	lines []string
	at    int // the focused line
}

// list is the other items of the focused bullet, one line each: the facts of a
// bullet are cumulative prefixes of it, so wrapping them all would push the
// focused one off the screen.
func (s *ledgerScreen) list(cur ledgerPos, w int) ledgerList {
	var l ledgerList
	for idx, p := range s.pos {
		if p.e != cur.e || p.b != cur.b {
			continue
		}
		mark, id, text := "• ", "", ""
		style := func(t ...string) string { return strings.Join(t, "") }
		pend := s.pending(p)
		if p.framing {
			fr := s.framing(p)
			id, text = "framing", fr.Phrase
			switch {
			case fr.Approved:
				mark, style = "✓ ", styOK.Render
			case fr.Rejected:
				mark, style = "✗ ", styDim.Render
			}
		} else {
			f := s.fact(p)
			id, text = f.ID[strings.LastIndex(f.ID, "/")+1:], f.Text
			switch f.Status {
			case "approved":
				mark, style = "✓ ", styOK.Render
			case "rejected":
				mark, style = "✗ ", styDim.Render
			}
			if f.Source == "inferred" {
				id += " inferred"
			}
		}
		prefix := "  "
		if idx == s.cur {
			prefix, l.at = styAccent.Render("▸ "), len(l.lines)
			if pend {
				style = styAccent.Render
			}
		}
		l.lines = append(l.lines, prefix+style(truncate(fmt.Sprintf("%s%-4s %s", mark, id, text), max(1, w-2))))
	}
	return l
}

// detail is what the focused item needs deciding: the whole text, and the lists
// the verifier will hold the model to.
func (s *ledgerScreen) detail(w int) []string {
	p := s.pos[s.cur]
	wrap := func(label, text string) []string {
		ls := strings.Split(wrapText(text, max(10, w-len(label))), "\n")
		for i := range ls {
			if i == 0 {
				ls[i] = label + ls[i]
			} else {
				ls[i] = strings.Repeat(" ", len(label)) + ls[i]
			}
		}
		return ls
	}
	if p.framing {
		fr := s.framing(p)
		return wrap("framing  ", fr.Phrase+"  ← "+strings.Join(fr.From, ", "))
	}
	f := s.fact(p)
	out := wrap("fact      ", f.Text)
	num := 0
	numbered := func(label string, xs []string) string {
		var parts []string
		for _, x := range xs {
			num++
			parts = append(parts, fmt.Sprintf("%d %s", num, x))
		}
		return truncate(label+strings.Join(parts, " · "), w)
	}
	if len(f.Entities) > 0 {
		out = append(out, numbered("entities  ", f.Entities))
	}
	if len(f.Figures) > 0 {
		out = append(out, numbered("figures   ", f.Figures))
	}
	keep := "no — may be dropped when a posting needs the room"
	if f.MustKeep {
		keep = "yes — every rewrite keeps it"
	}
	out = append(out, "must keep "+keep)
	switch {
	case s.edit != nil:
		out = append(out, s.edit.View())
	case s.del:
		out = append(out, styWarn.Render(fmt.Sprintf("remove which? 1-%d, any other key cancels", min(9, num))))
	}
	return out
}
