package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/suggest"
)

// tailorScreen is behind a finished row's "tailor" action: the CV's text as it
// was tailored to the posting, complete, with the model's second opinions marked
// on it. A suggestion changes nothing until it is accepted and the PDF is
// rebuilt — both are the person's call, so neither happens on its own.
type tailorScreen struct {
	l       paths.Layout
	id      int
	title   string
	dir     string
	targets []suggest.Target
	items   []suggest.Item
	cursor  int
	busy    string // what a spawned subcommand is doing, "" when idle
	dirty   bool   // decisions made since the PDF was last built
}

type tailorMsg struct {
	s   *tailorScreen
	err error
}

// tailorOpMsg is a spawned `snipe suggest` or `snipe rebuild` finishing.
type tailorOpMsg struct {
	op  string
	out string
	err error
}

func openTailor(l paths.Layout, c *state.Cache, info rowInfo) tea.Cmd {
	return func() tea.Msg {
		dir, ok := c.ResultDir(l.Output(), info.Report)
		if !ok {
			return tailorMsg{err: fmt.Errorf("#%d has no CV folder in output/", info.ID)}
		}
		body, err := os.ReadFile(filepath.Join(dir, "cv-content.json"))
		if err != nil {
			return tailorMsg{err: err}
		}
		content, err := fill.ParseContent(body)
		if err != nil {
			return tailorMsg{err: fmt.Errorf("cv-content.json: %w", err)}
		}
		s := &tailorScreen{l: l, id: info.ID, title: info.Label, dir: dir, targets: suggest.Targets(content)}
		if s.items, err = suggest.Load(dir); err != nil {
			return tailorMsg{err: err}
		}
		return tailorMsg{s: s}
	}
}

var tailorKeys = struct {
	Accept, Reject, Gen, Rebuild, Open key.Binding
}{
	Accept:  key.NewBinding(key.WithKeys("enter", "y"), key.WithHelp("enter", "accept")),
	Reject:  key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "reject")),
	Gen:     key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "suggest")),
	Rebuild: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rebuild the PDF")),
	Open:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open the PDF")),
}

func (s *tailorScreen) help() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("up/down"), key.WithHelp("↑↓", "move")),
		tailorKeys.Accept, tailorKeys.Reject, tailorKeys.Gen, tailorKeys.Rebuild, tailorKeys.Open,
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
	}
}

// at is the suggestion about a target, if there is one. Matched on the text it
// was made against, so a suggestion for text that has since changed matches
// nothing and stays out of sight.
func (s *tailorScreen) at(t suggest.Target) (suggest.Item, bool) {
	for _, it := range s.items {
		if it.Kind == t.Kind && it.Old == t.Text {
			return it, true
		}
	}
	return suggest.Item{}, false
}

func (s *tailorScreen) key(k tea.KeyPressMsg) tea.Cmd {
	if s.busy != "" {
		return nil
	}
	switch {
	case key.Matches(k, key.NewBinding(key.WithKeys("up", "k"))):
		s.cursor = max(0, s.cursor-1)
	case key.Matches(k, key.NewBinding(key.WithKeys("down", "j"))):
		s.cursor = min(len(s.targets)-1, s.cursor+1)
	case key.Matches(k, tailorKeys.Accept):
		return s.decide(suggest.Accepted)
	case key.Matches(k, tailorKeys.Reject):
		return s.decide(suggest.Rejected)
	case key.Matches(k, tailorKeys.Gen):
		s.busy = "asking the model for suggestions"
		return s.spawn("suggest")
	case key.Matches(k, tailorKeys.Rebuild):
		s.busy = "rebuilding the PDF"
		return s.spawn("rebuild")
	case key.Matches(k, tailorKeys.Open):
		if pdfs, _ := filepath.Glob(filepath.Join(s.dir, "*-CV.pdf")); len(pdfs) > 0 {
			return openExternal(pdfs[0])
		}
		return failf("no PDF in %s", s.dir)
	}
	return nil
}

// decide records accept or reject on the focused line, and clears it when that
// is already the decision — the undo for a stray key.
func (s *tailorScreen) decide(to string) tea.Cmd {
	if len(s.targets) == 0 {
		return nil
	}
	it, ok := s.at(s.targets[s.cursor])
	if !ok {
		return nil
	}
	if it.State == to {
		to = ""
	}
	if err := suggest.Mark(s.dir, it.ID, to); err != nil {
		return failf("%v", err)
	}
	for i := range s.items {
		if s.items[i].ID == it.ID {
			s.items[i].State = to
		}
	}
	s.dirty = true
	return nil
}

// spawn runs this binary's own subcommand for the offer. The model call and the
// LaTeX run take long enough that doing them here would freeze the screen.
func (s *tailorScreen) spawn(op string) tea.Cmd {
	l, id := s.l, s.id
	return func() tea.Msg {
		self, err := os.Executable()
		if err != nil {
			return tailorOpMsg{op: op, err: err}
		}
		cmd := exec.Command(self, op, strconv.Itoa(id))
		cmd.Dir = l.Code
		out, err := cmd.CombinedOutput()
		return tailorOpMsg{op: op, out: strings.TrimSpace(string(out)), err: err}
	}
}

// done takes a finished subcommand back in and returns the footer note.
func (s *tailorScreen) done(m tailorOpMsg) tea.Cmd {
	s.busy = ""
	if m.err != nil {
		return failf("%s: %s", m.op, firstNonEmpty(m.out, m.err.Error()))
	}
	switch m.op {
	case "suggest":
		items, err := suggest.Load(s.dir)
		if err != nil {
			return failf("%v", err)
		}
		s.items, s.dirty = items, false
		if len(items) == 0 {
			return notef("the model had no suggestions for this one")
		}
	case "rebuild":
		s.dirty = false
	}
	return notef("%s", m.out)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *tailorScreen) view(w, h int) string {
	status := "text as tailored — g asks the model for ATS rewordings"
	switch pend, acc := s.counts(); {
	case s.busy != "":
		status = s.busy + "…"
	case len(s.items) > 0:
		status = fmt.Sprintf("%d suggestion(s), %d accepted", len(s.items), acc)
		if pend == 0 {
			status += " — all decided"
		}
		if s.dirty {
			status += " · r rebuilds the PDF with them"
		}
	}
	head := []string{styHeading.Render(truncate(s.title, w)), styDim.Render(truncate(status, w))}

	detail := s.detail(w)
	listH := max(3, h-len(head)-len(detail)-1)

	var lines []string
	focusAt := 0
	where, kind := "", ""
	for i, t := range s.targets {
		if t.Where != where || t.Kind != kind {
			where, kind = t.Where, t.Kind
			label := t.Where
			if t.Kind == suggest.Skill {
				label = "Skills · " + label
			}
			lines = append(lines, styHeading.Render(truncate(label, w)))
		}
		if i == s.cursor {
			focusAt = len(lines)
		}
		lines = append(lines, s.render(i, t, w)...)
	}
	// Keep the focused target in view; if it is taller than the window, its top.
	start := max(0, focusAt-listH/3)
	if start+listH > len(lines) {
		start = max(0, len(lines)-listH)
	}
	end := min(len(lines), start+listH)

	out := append(head, lines[start:end]...)
	out = append(out, styDim.Render(strings.Repeat("─", max(0, w))))
	return strings.Join(append(out, detail...), "\n")
}

func (s *tailorScreen) counts() (pending, accepted int) {
	for _, it := range s.items {
		switch it.State {
		case "":
			pending++
		case suggest.Accepted:
			accepted++
		}
	}
	return
}

func (s *tailorScreen) render(i int, t suggest.Target, w int) []string {
	mark, text := "  ", t.Text
	style := func(strs ...string) string { return strings.Join(strs, "") }
	if it, ok := s.at(t); ok {
		switch it.State {
		case suggest.Accepted:
			mark, text, style = styOK.Render("✓ "), it.New, styOK.Render
		case suggest.Rejected:
			mark, style = styDim.Render("✗ "), styDim.Render
		default:
			mark = styAccent.Render("• ")
		}
	}
	cur := "  "
	if i == s.cursor {
		cur = styAccent.Render("▸ ")
	}
	body := strings.Split(wrapText(text, max(10, w-4-2)), "\n")
	out := make([]string, len(body))
	for j, ln := range body {
		if j == 0 {
			out[j] = cur + mark + style(ln)
		} else {
			out[j] = "    " + style(ln)
		}
	}
	return out
}

// detail is the focused suggestion in full: what is on the page, what is
// proposed, and the model's reason, quoted from the posting.
func (s *tailorScreen) detail(w int) []string {
	if len(s.targets) == 0 {
		return []string{styDim.Render("Nothing in this CV to review.")}
	}
	it, ok := s.at(s.targets[s.cursor])
	if !ok {
		return []string{styDim.Render("No suggestion for this line.")}
	}
	wrap := func(label, text string) []string {
		ls := strings.Split(wrapText(text, max(10, w-6)), "\n")
		for i := range ls {
			if i == 0 {
				ls[i] = label + ls[i]
			} else {
				ls[i] = "     " + ls[i]
			}
		}
		return ls
	}
	out := append(wrap("was  ", it.Old), wrap("now  ", it.New)...)
	if it.Why != "" {
		out = append(out, wrap("why  ", it.Why)...)
	}
	return out
}
