// Package adapt decides how each selected CV line may be adapted to a posting.
// It is the rewrite plan of docs/PRD-TAILORING-REWORK.md §6.2, built in code
// from the fact ledger (§6.1) and Phase 2's evidence rows (§5.2), under the
// 80/20 truth contract of §1.1. It makes no model call: the 30B that writes the
// drafts (§6.3) gets a narrow brief, and the verifier (§6.4) holds the drafts
// to it, so which tool may be claimed on which line is settled here and
// nowhere else.
package adapt

import (
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
)

// Kind is what stage 1 says a posting term is (PRD §5.2). Only tools and
// capabilities may be inferred; the rest are never claimed on the user's behalf.
type Kind string

const (
	KindUntyped     Kind = ""
	KindTool        Kind = "tool"
	KindCapability  Kind = "capability"
	KindDomain      Kind = "domain"
	KindCredential  Kind = "credential"
	KindEligibility Kind = "eligibility"
	KindSoft        Kind = "soft"
)

// Term is one posting term the rewrite must not get wrong.
type Term struct {
	Text string
	Kind Kind
}

// TermsFromParsed lists stage 1's output as it exists today. Stage 1 has no
// per-term type yet (PRD §5.2 adds `kind`), so tech_stack items are treated as
// tools and keywords stay untyped: they still gate Allowed and Forbidden, but
// are never proposed. Replace this when the typed parse lands.
func TermsFromParsed(p phase2.Parsed) []Term {
	var out []Term
	for _, t := range p.TechStack {
		out = append(out, Term{Text: t, Kind: KindTool})
	}
	for _, k := range p.Keywords {
		out = append(out, Term{Text: k})
	}
	return out
}

// RowsFromReport reads stage 2's rows back from a finished report, for the
// caller that has no checkpoint. The report keeps the grade per requirement but
// clips the evidence to 160 characters, which locate tolerates.
func RowsFromReport(report string) []phase2.Row {
	var out []phase2.Row
	for _, r := range cvselect.ExtractBlockB(report) {
		out = append(out, phase2.Row{
			Requirement: r.Requirement, MustHave: r.MustHave,
			Evidence: r.Evidence, Strength: r.Strength,
		})
	}
	return out
}

// Sim scores how close a requirement is to a fact, higher is closer. The caller
// passes an embedding-backed one; tests need no Ollama.
type Sim func(requirement, fact string) float64

// Posting is what the plan needs to know about the offer.
type Posting struct {
	ID    string // report number or batch id, stamped on queued inferences
	Date  string // YYYY-MM-DD, likewise
	Score float64
	// InferMinScore is profile.yml's infer_min_score; zero means the default.
	InferMinScore float64
	Rows          []phase2.Row
	Terms         []Term
	// Sim picks the fact that leads a line. Nil falls back to word overlap,
	// which is enough to choose between facts of one bullet.
	Sim Sim
	// AlignMin lets a bullet that stage 2 never picked as evidence still target
	// a requirement, when Sim rates one of its facts at or above it. Zero turns
	// that off, since the cut-off depends on the embedder and has not been
	// measured.
	AlignMin float64
}

func (p Posting) minScore() float64 {
	if p.InferMinScore > 0 {
		return p.InferMinScore
	}
	return config.DefaultInferMinScore
}

// Target is a requirement the line can speak to.
type Target struct {
	Requirement string
	Must        bool
	Strength    string // fit.Strong or fit.Transferable
	Fact        string // id of the bullet's fact closest to it, "" if none
}

// Proposal is a posting tool that may be an inference about this entry,
// waiting for the user's yes.
type Proposal struct {
	Term        string
	Requirement string // the requirement that named it and graded Transferable here
}

// MustKeep is what a rewrite may not lose.
type MustKeep struct {
	Facts      []string // fact ids
	Figures    []string
	Qualifiers []string // the entry's: "Supervised Project"
}

// Plan is the brief for one selected bullet.
type Plan struct {
	EntryID string
	Bullet  ledger.Bullet
	Targets []Target
	Lead    string
	// AllowedTerms are posting terms reachable from this bullet's facts, an
	// approved framing of it, or an inference approved for its entry.
	AllowedTerms []string
	Proposed     []Proposal
	// Forbidden is every other posting term, proposals included: a proposal is
	// not a claim until it is approved.
	Forbidden []string
	MustKeep  MustKeep
	MaxLines  int
	// Verbatim marks a bullet the ledger does not know (cv.md changed since the
	// last build). Nothing is known about its facts, so it ships unchanged.
	Verbatim bool
}

// Terms is every posting term the plan has an opinion on, allowed first, for
// rendering in the prompt.
func (p Plan) Terms() []string {
	var out []string
	for _, t := range p.AllowedTerms {
		out = appendFold(out, t)
	}
	for _, t := range p.Proposed {
		out = appendFold(out, t.Term)
	}
	for _, t := range p.Forbidden {
		out = appendFold(out, t)
	}
	return out
}

// PlanAll plans each selected bullet, in selection order. sel holds the bullet
// texts exactly as cv.md has them.
func PlanAll(l *ledger.Ledger, sel []string, p Posting) []Plan {
	out := make([]Plan, len(sel))
	for i, text := range sel {
		out[i] = BuildPlan(l, text, p)
	}
	return out
}

// BuildPlan plans one bullet.
func BuildPlan(l *ledger.Ledger, text string, p Posting) Plan {
	plan := Plan{MaxLines: cvselect.BulletLines(text)}
	e, b := find(l, text)
	if b == nil {
		plan.Bullet = ledger.Bullet{Text: text}
		plan.Verbatim = true
		return plan
	}
	plan.EntryID, plan.Bullet = e.ID, *b

	var facts []ledger.Fact
	for _, f := range b.Facts {
		if f.Source != "inferred" {
			facts = append(facts, f)
		}
	}
	plan.Targets = targets(l, b, facts, p)
	plan.Lead = lead(b, facts, plan.Targets)
	plan.MustKeep = mustKeep(e, facts)

	status := inferenceStatus(e)
	here := evidenceHere(l, b, p.Rows)
	for _, t := range dedupeTerms(p.Terms) {
		if reachable(l, t.Text, facts, b.Framings, status.approved) {
			plan.AllowedTerms = appendFold(plan.AllowedTerms, t.Text)
			continue
		}
		if p.Score >= p.minScore() && (t.Kind == KindTool || t.Kind == KindCapability) &&
			!mentions(status.rejected, l, t.Text) {
			if req, ok := sameWork(here, t.Text); ok {
				plan.Proposed = append(plan.Proposed, Proposal{Term: t.Text, Requirement: req})
			}
		}
		plan.Forbidden = appendFold(plan.Forbidden, t.Text)
	}
	// A framing is the posting's phrase for something the facts already say.
	// It is allowed when the posting asks for it, even if stage 1 never listed
	// it as a keyword.
	for _, fr := range b.Framings {
		if fr.Approved && !fr.Rejected && postingAsks(p, fr.Phrase) {
			plan.AllowedTerms = appendFold(plan.AllowedTerms, fr.Phrase)
			plan.Targets = framingTargets(plan.Targets, p, fr)
		}
	}
	sortTargets(plan.Targets)
	return plan
}

// framingTargets adds every requirement that names an approved framing of this
// line. Stage 2 hands each requirement its three nearest bullets, so a line
// whose facts the user has approved as "microservices" can still lose the
// requirement asking for microservices to two other bullets. The first E3 run
// with framings approved showed the cost: a draft that wrote "10 Java and
// Spring Boot microservices" scored no better than the original, because no
// target named the word, and the original won the tie.
func framingTargets(ts []Target, p Posting, fr ledger.Framing) []Target {
	for _, r := range p.Rows {
		if !contains(r.Requirement, fr.Phrase) ||
			slices.ContainsFunc(ts, func(t Target) bool { return t.Requirement == r.Requirement }) {
			continue
		}
		t := Target{Requirement: r.Requirement, Must: r.MustHave, Strength: fit.Transferable}
		if len(fr.From) > 0 {
			t.Fact = fr.From[0]
		}
		ts = append(ts, t)
	}
	return ts
}

// find locates a bullet by the hash its approvals hang on. The holding bullet
// for approved inferences is not a line anyone selects.
func find(l *ledger.Ledger, text string) (*ledger.Entry, *ledger.Bullet) {
	if l == nil {
		return nil, nil
	}
	h := ledger.Hash(text)
	for i := range l.Entries {
		for j := range l.Entries[i].Bullets {
			if b := &l.Entries[i].Bullets[j]; b.Hash == h && b.Hash != ledger.InferredHash {
				return &l.Entries[i], b
			}
		}
	}
	return nil, nil
}

// ── Targets and lead ─────────────────────────────────────────────────────────

// targets are the requirements stage 2 picked this bullet as evidence for.
// Stage 2 is the source because it already graded them, and a Gap row stays a
// gap on the page (T3): no line is pointed at a requirement the CV does not
// meet. When stage 2 never picked the bullet and the caller gave a Sim and a
// cut-off, the closest requirements graded above Gap stand in.
func targets(l *ledger.Ledger, b *ledger.Bullet, facts []ledger.Fact, p Posting) []Target {
	var out []Target
	for _, r := range p.Rows {
		if r.Strength != fit.Gap && locate(l, r.Evidence) == b {
			out = append(out, Target{Requirement: r.Requirement, Must: r.MustHave, Strength: r.Strength})
		}
	}
	if len(out) == 0 && p.Sim != nil && p.AlignMin > 0 {
		type scored struct {
			t Target
			s float64
		}
		var near []scored
		for _, r := range p.Rows {
			if r.Strength == fit.Gap {
				continue
			}
			if s, _ := closest(p.Sim, r.Requirement, facts); s >= p.AlignMin {
				near = append(near, scored{Target{Requirement: r.Requirement, Must: r.MustHave, Strength: r.Strength}, s})
			}
		}
		sort.SliceStable(near, func(i, j int) bool { return near[i].s > near[j].s })
		for _, n := range near[:min(2, len(near))] {
			out = append(out, n.t)
		}
	}
	sortTargets(out)
	for i := range out {
		_, out[i].Fact = closest(p.Sim, out[i].Requirement, facts)
	}
	return out
}

// sortTargets puts must-haves first, then the stronger evidence; stage 2's
// order breaks ties.
func sortTargets(out []Target) {
	rank := func(t Target) int {
		r := 0
		if !t.Must {
			r += 2
		}
		if t.Strength != fit.Strong {
			r++
		}
		return r
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
}

// closest is the fact nearest a requirement. A score of zero is no fact: a line
// is not re-led around a fact that shares nothing with the target.
func closest(sim Sim, req string, facts []ledger.Fact) (float64, string) {
	if sim == nil {
		sim = overlap
	}
	best, id := 0.0, ""
	for _, f := range facts {
		if s := sim(req, f.Text); s > best {
			best, id = s, f.ID
		}
	}
	return best, id
}

// lead is the top target's fact, unless the source already opens with it.
func lead(b *ledger.Bullet, facts []ledger.Fact, ts []Target) string {
	if len(ts) == 0 || ts[0].Fact == "" {
		return ""
	}
	id := ts[0].Fact
	for i, f := range facts {
		if f.ID == id && (i == 0 || strings.HasPrefix(strings.ToLower(b.Text), strings.ToLower(f.Text))) {
			return ""
		}
	}
	return id
}

func mustKeep(e *ledger.Entry, facts []ledger.Fact) MustKeep {
	m := MustKeep{Qualifiers: slices.Clone(e.Qualifiers)}
	for _, f := range facts {
		if f.MustKeep {
			m.Facts = append(m.Facts, f.ID)
		}
		for _, fig := range f.Figures {
			if !slices.Contains(m.Figures, fig) {
				m.Figures = append(m.Figures, fig)
			}
		}
	}
	return m
}

// ── Terms ────────────────────────────────────────────────────────────────────

type inferences struct{ approved, rejected []ledger.Fact }

// inferenceStatus gathers the entry's inferred facts. They belong to the entry,
// not the line they were first filed under (PRD §1.1), so every bullet sees
// them. A pending one counts as neither: it is not yet a claim, and not yet a
// refusal.
func inferenceStatus(e *ledger.Entry) inferences {
	var s inferences
	for _, b := range e.Bullets {
		for _, f := range b.Facts {
			if f.Source != "inferred" {
				continue
			}
			switch f.Status {
			case "approved":
				s.approved = append(s.approved, f)
			case "rejected":
				s.rejected = append(s.rejected, f)
			}
		}
	}
	return s
}

// reachable is true when the term has a path from the line: an entity form of
// something its facts name, an approved framing, or an approved inference.
func reachable(l *ledger.Ledger, term string, facts []ledger.Fact, framings []ledger.Framing, approved []ledger.Fact) bool {
	for _, fr := range framings {
		if fr.Approved && !fr.Rejected && strings.EqualFold(fr.Phrase, term) {
			return true
		}
	}
	return mentions(facts, l, term) || mentions(approved, l, term)
}

// mentions is true when one of the facts names the term, or another spelling
// the ledger records for the same entity (Postgres for PostgreSQL).
func mentions(fs []ledger.Fact, l *ledger.Ledger, term string) bool {
	forms := formsOf(l, term)
	return slices.ContainsFunc(fs, func(f ledger.Fact) bool {
		return slices.ContainsFunc(forms, func(form string) bool {
			return contains(f.Text, form) ||
				slices.ContainsFunc(f.Entities, func(e string) bool { return strings.EqualFold(e, form) })
		})
	})
}

func formsOf(l *ledger.Ledger, term string) []string {
	forms := []string{term}
	if l == nil {
		return forms
	}
	for canon, group := range l.Entities {
		all := append([]string{canon}, group...)
		if slices.ContainsFunc(all, func(s string) bool { return strings.EqualFold(s, term) }) {
			for _, s := range all {
				forms = appendFold(forms, s)
			}
		}
	}
	return forms
}

// sameWork is condition 3 of §1.1: stage 2 graded a requirement naming the term
// Transferable against this very line, same activity with other tooling. Tied
// to the line and not the entry so that Kubernetes lands on the deployment
// bullet and never on the billing one; approving it then opens it to the entry.
func sameWork(here []phase2.Row, term string) (string, bool) {
	for _, r := range here {
		if r.Strength == fit.Transferable && contains(r.Requirement, term) {
			return r.Requirement, true
		}
	}
	return "", false
}

func postingAsks(p Posting, phrase string) bool {
	for _, r := range p.Rows {
		if contains(r.Requirement, phrase) {
			return true
		}
	}
	for _, t := range p.Terms {
		if strings.EqualFold(t.Text, phrase) {
			return true
		}
	}
	return false
}

// ── Evidence ─────────────────────────────────────────────────────────────────

// probe is how much of a bullet identifies it inside an evidence string. Stage
// 2's atoms are "<entry header>: <bullet>", the report clips them at 160, and
// the entry header is spelled differently there than in the ledger, so the
// bullet's own opening is the key.
const probe = 50

func norm(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(s, "|", "/")), " "))
}

// locate maps an evidence string back to its ledger bullet, nil when it is a
// catalogue line, a summary sentence, "—", or text the ledger has moved past.
func locate(l *ledger.Ledger, evidence string) *ledger.Bullet {
	ev := norm(evidence)
	if l == nil || len(ev) < 20 {
		return nil
	}
	for i := range l.Entries {
		for j := range l.Entries[i].Bullets {
			b := &l.Entries[i].Bullets[j]
			if b.Hash == ledger.InferredHash {
				continue
			}
			k := []rune(norm(b.Text))
			if len(k) > 0 && strings.Contains(ev, string(k[:min(len(k), probe)])) {
				return b
			}
		}
	}
	return nil
}

func evidenceHere(l *ledger.Ledger, b *ledger.Bullet, rows []phase2.Row) []phase2.Row {
	var out []phase2.Row
	for _, r := range rows {
		if locate(l, r.Evidence) == b {
			out = append(out, r)
		}
	}
	return out
}

// ── Queue ────────────────────────────────────────────────────────────────────

// Candidate is a proposed inference ready for the approval queue.
type Candidate struct {
	EntryID string
	Why     string      // the requirement it answers
	Fact    ledger.Fact // Source "inferred", Status "", Approved "<posting>@<date>"
}

// Candidates turns every proposal across the plans into a ledger fact, once
// per entry and term. It saves nothing: the caller owns the ledger file, the
// fact id (which needs the entry's counter) and the approval. Approved holds
// the posting and date the term was proposed on, so approving it later needs
// only a Status.
//
// A term the entry already has a pending fact for is left out. A rejected one
// never reaches here, BuildPlan having refused to propose it.
func Candidates(l *ledger.Ledger, plans []Plan, p Posting) []Candidate {
	var out []Candidate
	for _, pl := range plans {
		var e *ledger.Entry
		for i := range l.Entries {
			if l.Entries[i].ID == pl.EntryID {
				e = &l.Entries[i]
			}
		}
		for _, pr := range pl.Proposed {
			if e == nil || mentions(pending(e), l, pr.Term) ||
				slices.ContainsFunc(out, func(c Candidate) bool {
					return c.EntryID == pl.EntryID && strings.EqualFold(c.Fact.Entities[0], pr.Term)
				}) {
				continue
			}
			out = append(out, Candidate{
				EntryID: pl.EntryID, Why: pr.Requirement,
				Fact: ledger.Fact{
					Text: "Used " + pr.Term, Entities: []string{pr.Term},
					Source: "inferred", Approved: p.ID + "@" + p.Date,
				},
			})
		}
	}
	return out
}

// FramingCandidate is a posting phrase proposed as a framing of one line: stage
// 2 graded a requirement naming it Strong against this very line, yet the line
// never says it. The framing is the user's call, so it is filed undecided.
type FramingCandidate struct {
	EntryID    string
	BulletHash string
	Why        string // the requirement it answers
	Framing    ledger.Framing
}

// FramingCandidates proposes, per line, the posting's own phrase for work the
// line already states. Unlike an inference it adds no claim, so the score bar
// does not apply; unlike the corpus pass it asks only about phrases this
// posting used. A phrase the line has a framing for in any state, rejections
// included, is not proposed again.
func FramingCandidates(l *ledger.Ledger, plans []Plan, p Posting) []FramingCandidate {
	var out []FramingCandidate
	for _, pl := range plans {
		if pl.Verbatim {
			continue
		}
		_, b := find(l, pl.Bullet.Text) // pl.Bullet is a copy; locate compares addresses
		here := evidenceHere(l, b, p.Rows)
		for _, t := range dedupeTerms(p.Terms) {
			if t.Kind != KindUntyped && t.Kind != KindCapability {
				continue
			}
			if !slices.ContainsFunc(pl.Forbidden, func(f string) bool { return strings.EqualFold(f, t.Text) }) ||
				slices.ContainsFunc(pl.Bullet.Framings, func(f ledger.Framing) bool { return strings.EqualFold(f.Phrase, t.Text) }) ||
				!l.PhraseSafe(t.Text, pl.Bullet.Text) {
				continue
			}
			for _, r := range here {
				if r.Strength != fit.Strong || !contains(r.Requirement, t.Text) {
					continue
				}
				// The fact nearest the requirement. None means the line shares
				// nothing with it, and stage 2's Strong was a misgrade: a teaching
				// bullet graded Strong for "drive engineering excellence by
				// co-designing" would otherwise yield "engineering excellence".
				i := slices.IndexFunc(pl.Targets, func(x Target) bool { return x.Requirement == r.Requirement })
				if i >= 0 && pl.Targets[i].Fact != "" {
					out = append(out, FramingCandidate{
						EntryID: pl.EntryID, BulletHash: pl.Bullet.Hash, Why: r.Requirement,
						Framing: ledger.Framing{Phrase: t.Text, From: []string{pl.Targets[i].Fact}},
					})
				}
				break
			}
		}
	}
	return out
}

func pending(e *ledger.Entry) []ledger.Fact {
	var out []ledger.Fact
	for _, b := range e.Bullets {
		for _, f := range b.Facts {
			if f.Source == "inferred" && f.Status == "" {
				out = append(out, f)
			}
		}
	}
	return out
}

// ── Text ─────────────────────────────────────────────────────────────────────

// contains is a whole-word, case-insensitive match, so Kafka is not found in
// "Kafkaesque" and a symbol-bearing term like C++ still matches. Terms of three
// characters or fewer (Go, AWS, CI) match case-sensitively: "go" is a word.
func contains(s, term string) bool {
	if term == "" {
		return false
	}
	if utf8.RuneCountInString(term) > 3 {
		s, term = strings.ToLower(s), strings.ToLower(term)
	}
	for from := 0; from < len(s); {
		i := strings.Index(s[from:], term)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(term)
		before, _ := utf8.DecodeLastRuneInString(s[:i])
		after, _ := utf8.DecodeRuneInString(s[end:])
		// Empty edges decode to RuneError, which is not a word character.
		if !isWord(before) && !isWord(after) {
			return true
		}
		from = i + 1
	}
	return false
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func appendFold(xs []string, s string) []string {
	if s == "" || slices.ContainsFunc(xs, func(x string) bool { return strings.EqualFold(x, s) }) {
		return xs
	}
	return append(xs, s)
}

func dedupeTerms(ts []Term) []Term {
	var out []Term
	for _, t := range ts {
		if strings.TrimSpace(t.Text) == "" {
			continue
		}
		if i := slices.IndexFunc(out, func(o Term) bool { return strings.EqualFold(o.Text, t.Text) }); i >= 0 {
			// A typed copy beats an untyped one: the keyword list repeats tech_stack.
			if out[i].Kind == KindUntyped {
				out[i].Kind = t.Kind
			}
			continue
		}
		out = append(out, t)
	}
	return out
}

var stop = map[string]bool{"the": true, "and": true, "for": true, "with": true, "that": true, "from": true, "into": true, "using": true}

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !isWord(r) }) {
		if len(w) >= 3 && !stop[w] {
			m[w] = true
		}
	}
	return m
}

// overlap is the default Sim: shared words over the geometric mean of both
// sizes. Crude, and only ever used to rank the facts of one bullet.
func overlap(a, b string) float64 {
	wa, wb := words(a), words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	n := 0
	for w := range wa {
		if wb[w] {
			n++
		}
	}
	return float64(n) / math.Sqrt(float64(len(wa)*len(wb)))
}
