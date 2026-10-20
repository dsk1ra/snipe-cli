package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/dsk1ra/snipe-cli/internal/aliases"
	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase3"
)

// skillsReview is the screen behind a finished row's "skills" action, which
// took the report's place. The report is the model's reasoning and nobody read
// it; this is the one decision the pipeline still needs from a person.
//
// It shows the skills block the posting would get if tailored now, then the
// alias queue with this posting's terms first. A decision is global — an alias
// reaches every CV from then on — so the list is the whole queue, and the
// posting only sets the order and supplies the quotes.
type skillsReview struct {
	l     paths.Layout
	id    int
	title string
	jd    string
	cfg   *config.Config // cv.md and the aliases as of the last decision

	rows   []aliases.Suggestion
	mine   int      // rows[:mine] are terms from this posting
	pick   []int    // the target each row would be added under
	state  []string // "", "added" or "rejected", this session only
	cursor int

	jds    map[int]string // posting text by id, read as rows need quoting
	quotes []string
}

type reviewMsg struct {
	r   *skillsReview
	err error
}

// reviewDoneMsg is one decision landing on disk.
type reviewDoneMsg struct {
	row   int
	state string
	cfg   *config.Config // reloaded after an add, so the block shows it
	note  string
	err   error
}

func openSkills(l paths.Layout, info rowInfo) tea.Cmd {
	return func() tea.Msg {
		cfg, err := config.Load(l)
		if err != nil {
			return reviewMsg{err: err}
		}
		all, err := aliases.Load(l.SkillSuggestions())
		if err != nil {
			return reviewMsg{err: err}
		}
		jd, _ := os.ReadFile(filepath.Join(l.JDs(), strconv.Itoa(info.ID)+".txt"))
		var mine, rest []aliases.Suggestion
		for _, s := range all {
			switch {
			case s.Rejected:
			case slices.Contains(s.IDs, info.ID):
				mine = append(mine, s)
			default:
				rest = append(rest, s)
			}
		}
		rows := append(mine, rest...)
		r := &skillsReview{
			l: l, id: info.ID, title: info.Label, jd: string(jd), cfg: cfg,
			rows: rows, mine: len(mine),
			pick: make([]int, len(rows)), state: make([]string, len(rows)),
			jds: map[int]string{info.ID: string(jd)},
		}
		r.quote()
		return reviewMsg{r: r}
	}
}

var reviewKeys = struct {
	Add, Reject, Item key.Binding
}{
	Add:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "add the spelling")),
	Reject: key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "not an alias")),
	Item:   key.NewBinding(key.WithKeys("left", "right"), key.WithHelp("←→", "other item")),
}

func (r *skillsReview) help() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("up/down"), key.WithHelp("↑↓", "move")),
		reviewKeys.Add, reviewKeys.Reject, reviewKeys.Item,
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
	}
}

func (r *skillsReview) key(k tea.KeyPressMsg) tea.Cmd {
	if len(r.rows) == 0 {
		return nil
	}
	switch k.String() {
	case "up", "k":
		r.move(-1)
	case "down", "j":
		r.move(1)
	case "left":
		r.pick[r.cursor] = (r.pick[r.cursor] + len(r.rows[r.cursor].Targets) - 1) % len(r.rows[r.cursor].Targets)
	case "right":
		r.pick[r.cursor] = (r.pick[r.cursor] + 1) % len(r.rows[r.cursor].Targets)
	case "enter":
		return r.add()
	case "n":
		return r.reject()
	}
	return nil
}

func (r *skillsReview) move(d int) {
	r.cursor = max(0, min(len(r.rows)-1, r.cursor+d))
	r.quote()
}

// add writes the spelling under the chosen item. A rejected row can still be
// added this session, which is the undo for a stray n.
func (r *skillsReview) add() tea.Cmd {
	i := r.cursor
	if r.state[i] == "added" {
		return nil
	}
	s, item := r.rows[i], r.rows[i].Targets[r.pick[i]].Item
	if strings.Contains(item, " / ") {
		return failf("%q is more than one skill, and an alias replaces the whole item. Split it in cv.md, or ←→ for another", item)
	}
	l := r.l
	return func() tea.Msg {
		if err := aliases.Accept(l, item, s.Term); err != nil {
			return reviewDoneMsg{row: i, err: err}
		}
		cfg, err := config.Load(l)
		return reviewDoneMsg{row: i, state: "added", cfg: cfg, err: err,
			note: fmt.Sprintf("%q now prints for %s — config/skills.yml", s.Term, item)}
	}
}

func (r *skillsReview) reject() tea.Cmd {
	i := r.cursor
	if r.state[i] != "" {
		return nil
	}
	path, term := r.l.SkillSuggestions(), r.rows[i].Term
	return func() tea.Msg {
		return reviewDoneMsg{row: i, state: "rejected", err: aliases.Reject(path, term),
			note: fmt.Sprintf("%q will not be suggested again", term)}
	}
}

// done records a decision and moves on to the next row.
func (r *skillsReview) done(m reviewDoneMsg) {
	r.state[m.row] = m.state
	if m.cfg != nil {
		r.cfg = m.cfg
	}
	if m.row == r.cursor {
		r.move(1)
	}
}

// quote finds the focused term in up to two postings, this one first.
func (r *skillsReview) quote() {
	r.quotes = nil
	if len(r.rows) == 0 {
		return
	}
	s := r.rows[r.cursor]
	ids := s.IDs
	if i := slices.Index(ids, r.id); i > 0 {
		ids = append([]int{r.id}, slices.Delete(slices.Clone(ids), i, i+1)...)
	}
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(s.Term))
	for _, id := range ids {
		if len(r.quotes) == 2 {
			break
		}
		text, ok := r.jds[id]
		if !ok {
			b, _ := os.ReadFile(filepath.Join(r.l.JDs(), strconv.Itoa(id)+".txt"))
			text = string(b)
			r.jds[id] = text
		}
		if q := snippet(text, re); q != "" {
			r.quotes = append(r.quotes, fmt.Sprintf("#%-4d %s", id, q))
		}
	}
}

// snippet is the match with about fifty characters either side, on one line.
func snippet(text string, re *regexp.Regexp) string {
	loc := re.FindStringIndex(text)
	if loc == nil {
		return ""
	}
	from, to := max(0, loc[0]-50), min(len(text), loc[1]+50)
	for from > 0 && !utf8.RuneStart(text[from]) {
		from--
	}
	for to < len(text) && !utf8.RuneStart(text[to]) {
		to++
	}
	q := strings.Join(strings.Fields(text[from:to]), " ")
	if from > 0 {
		q = "…" + q
	}
	if to < len(text) {
		q += "…"
	}
	return `"` + q + `"`
}

func (r *skillsReview) view(w, h int) string {
	out := []string{styHeading.Render(truncate(r.title, w)), styDim.Render("skills block if tailored now")}
	for _, c := range phase3.SelectSkills(r.cfg.CVRaw, r.jd, phase3.SkillCats, phase3.SkillItems, phase3.SkillFloor, r.cfg.Aliases) {
		out = append(out, truncate("  "+c.Category+": "+c.Items, w))
	}
	out = append(out, "")
	if len(r.rows) == 0 {
		return strings.Join(append(out, styDim.Render("Nothing to review. The list is rebuilt after every run.")), "\n")
	}
	out = append(out,
		styHeading.Render(truncate(fmt.Sprintf("spellings to review · %d from this posting (•) · %d in all", r.mine, len(r.rows)), w)),
		styDim.Render(truncate(fmt.Sprintf("    %-24s   %-32s %4s %4s", "postings say", "cv.md item", "sim", "jds"), w)))

	detail := r.detail(w)
	listH := max(3, h-len(out)-len(detail)-1)
	start := max(0, r.cursor-listH+1)
	for i := start; i < len(r.rows) && i < start+listH; i++ {
		out = append(out, r.line(i, w))
	}
	out = append(out, styDim.Render(strings.Repeat("─", max(0, w))))
	return strings.Join(append(out, detail...), "\n")
}

func (r *skillsReview) line(i, w int) string {
	s := r.rows[i]
	cur, here := "  ", "  "
	if i == r.cursor {
		cur = "▸ "
	}
	if i < r.mine {
		here = "• "
	}
	t := s.Targets[r.pick[i]]
	body := fmt.Sprintf("%-24s → %-32s %.2f %4d", truncate(s.Term, 24), truncate(t.Item, 32), t.Sim, len(s.IDs))
	line := truncate(cur+here+body, w)
	switch r.state[i] {
	case "added":
		return styOK.Render(truncate(line+"  added", w))
	case "rejected":
		return styDim.Render(truncate(line+"  not an alias", w))
	}
	if strings.Contains(t.Item, " / ") {
		return line + styWarn.Render(truncate("  split in cv.md first", max(0, w-len([]rune(line)))))
	}
	if i == r.cursor {
		return styAccent.Render(line)
	}
	return line
}

// detail is what the focused row needs to be decided: where the postings use
// the term, the items it could go under, and the traps it may be.
func (r *skillsReview) detail(w int) []string {
	s := r.rows[r.cursor]
	var out []string
	for _, q := range r.quotes {
		out = append(out, truncate(q, w))
	}
	var ts []string
	for i, t := range s.Targets {
		label := fmt.Sprintf("%s %.2f", t.Item, t.Sim)
		if i == r.pick[r.cursor] {
			label = styAccent.Render("[" + label + "]")
		}
		ts = append(ts, label)
	}
	out = append(out, "under: "+strings.Join(ts, " · "))
	if s.Lower > 0 {
		out = append(out, styWarn.Render(truncate(fmt.Sprintf(
			"%d postings use %q only in lowercase. If it is an ordinary word there, the alias matches it too", s.Lower, s.Term), w)))
	}
	return out
}
