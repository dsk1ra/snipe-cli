package adapt

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/verify"
)

// Selection is PRD §6.5: among the drafts that survived verification, plus the
// original, ship the one that reads best to the posting, unless it would push
// its role past the 20% inferred budget of §1.1. It is code and it is
// deterministic: the same drafts in any order give the same line, so a rerun
// of an offer cannot reshuffle what the user already reviewed.

// MaxInferredShare is §1.1's budget: at most this share of a role's claims may
// be inferred from the posting.
const MaxInferredShare = 0.20

// headWords is how far into a line "front-loaded" reaches (§6.5: ~10 words).
// An ATS and a skimming reader both weigh the start of the line most.
const headWords = 10

// Verified is a draft that passed verify.Verify, with the class it passed as.
type Verified struct {
	Draft Draft
	Class verify.Class
}

// Choice is what ships for one bullet.
type Choice struct {
	Entry    string // ledger entry id; "" when the ledger did not know the bullet
	Hash     string // ledger hash of the source bullet
	Original string
	Text     string
	Source   string       // "original", "A" or "B"
	Class    verify.Class // Supported for the original
	Why      string
	Claims   Claims
}

// Tally is the running 80/20 position of one entry while its bullets are being
// chosen. Supported and Inferred must be the entry's totals as it would ship
// with every bullet not yet decided at its original, because a bullet's
// inferred claim is judged against the whole role and not against the bullets
// seen so far: one inferred claim among the first bullet's four is no breach if
// the role has fifteen. Select swaps the bullet's original for the choice.
type Tally struct {
	Supported, Inferred int
	Scope
}

// Scope is what the entry's approved inferences are, needed to see which of
// them a line still names.
type Scope struct {
	Approved []ledger.Fact       // the entry's approved inferred facts
	Forms    map[string][]string // ledger.Ledger.Entities
}

// ScopeOf collects the approved inferences of one entry.
func ScopeOf(l *ledger.Ledger, entryID string) Scope {
	var s Scope
	if l == nil {
		return s
	}
	s.Forms = l.Entities
	for i := range l.Entries {
		if l.Entries[i].ID == entryID {
			s.Approved = inferenceStatus(&l.Entries[i]).approved
		}
	}
	return s
}

// Select picks the line for one bullet. The original wins every tie, and a
// draft that does not beat it on evidence, or on parity when evidence is level,
// is not worth the risk of a changed line.
func Select(plan Plan, original string, survivors []Verified, t Tally) Choice {
	base := claimsOf(plan, original, verify.Supported, original, t.Scope)
	keep := Choice{
		Entry: plan.EntryID, Hash: plan.Bullet.Hash, Original: original, Text: original,
		Source: "original", Class: verify.Supported, Claims: base,
	}
	keep.Why = "original: no draft beats it"
	if plan.Verbatim {
		keep.Why = "original: the ledger does not know this line"
		return keep
	}

	// Sort first so nothing below depends on arrival order.
	cands := slices.Clone(survivors)
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.Draft.Kind != b.Draft.Kind {
			return a.Draft.Kind < b.Draft.Kind
		}
		return a.Draft.Text < b.Draft.Text
	})

	bestScore := evidence(plan, original)
	bestPar := 0
	var best *Choice
	var skipped []string
	for _, v := range cands {
		if (v.Class != verify.Supported && v.Class != verify.Inferred) ||
			strings.TrimSpace(v.Draft.Text) == "" || v.Draft.Text == original {
			continue
		}
		c := claimsOf(plan, v.Draft.Text, v.Class, original, t.Scope)
		if over(t.Supported-base.Supported+c.Supported, t.Inferred-base.Inferred+c.Inferred) {
			skipped = append(skipped, v.Draft.Kind+" would take the role past 20% inferred")
			continue
		}
		ev, par := evidence(plan, v.Draft.Text), parity(plan, original, v.Draft.Text, t.Scope)
		if !better(ev, par, c.Inferred, bestScore, bestPar, best) {
			continue
		}
		bestScore, bestPar = ev, par
		best = &Choice{
			Entry: plan.EntryID, Hash: plan.Bullet.Hash, Original: original, Text: v.Draft.Text,
			Source: v.Draft.Kind, Class: v.Class, Claims: c,
			Why: v.Draft.Kind + ": " + why(ev, par, c),
		}
	}
	if best != nil {
		return *best
	}
	if len(skipped) > 0 {
		keep.Why = "original: " + strings.Join(skipped, "; ")
	}
	return keep
}

// better is a strict win over the incumbent: the original at first, then the
// best draft so far. Among drafts level on both scores the one with fewer
// inferred claims stays (cands is sorted, so after that "A", the smaller edit,
// stays too); against the original a level draft never wins.
func better(ev, par, inferred, bestEv, bestPar int, best *Choice) bool {
	if ev != bestEv {
		return ev > bestEv
	}
	if par != bestPar {
		return par > bestPar
	}
	return best != nil && inferred < best.Claims.Inferred
}

func why(ev, par int, c Claims) string {
	s := fmt.Sprintf("evidence %d, parity +%d", ev, par)
	if c.Inferred > 0 {
		s += fmt.Sprintf(", %d inferred", c.Inferred)
	}
	return s
}

// SelectAll chooses every bullet of an offer, in plan order, keeping each
// entry's tally honest as it goes. survivors[i] belongs to plans[i].
func SelectAll(l *ledger.Ledger, plans []Plan, survivors [][]Verified) []Choice {
	tallies := map[string]*Tally{}
	for _, p := range plans {
		if p.Verbatim {
			continue
		}
		t := tallies[p.EntryID]
		if t == nil {
			t = &Tally{Scope: ScopeOf(l, p.EntryID)}
			tallies[p.EntryID] = t
		}
		c := claimsOf(p, p.Bullet.Text, verify.Supported, p.Bullet.Text, t.Scope)
		t.Supported += c.Supported
	}
	out := make([]Choice, len(plans))
	for i, p := range plans {
		var sv []Verified
		if i < len(survivors) {
			sv = survivors[i]
		}
		t := tallies[p.EntryID]
		if t == nil { // verbatim: no entry, nothing to tally
			t = &Tally{}
		}
		out[i] = Select(p, p.Bullet.Text, sv, *t)
		if p.Verbatim {
			continue
		}
		base := claimsOf(p, p.Bullet.Text, verify.Supported, p.Bullet.Text, t.Scope)
		t.Supported += out[i].Claims.Supported - base.Supported
		t.Inferred += out[i].Claims.Inferred - base.Inferred
	}
	return out
}

// over is the budget test in integers so that exactly 20% passes.
func over(supported, inferred int) bool {
	return float64(inferred) > MaxInferredShare*float64(supported+inferred)+1e-9
}

// evidence weighs the targets the line now speaks to: a target counts when a
// posting term that belongs to it (an allowed term the requirement names, which
// includes approved framings) opens the line. Must-haves count double.
func evidence(plan Plan, text string) int {
	head := text
	if f := strings.Fields(text); len(f) > headWords {
		head = strings.Join(f[:headWords], " ")
	}
	n := 0
	for _, t := range plan.Targets {
		for _, term := range plan.AllowedTerms {
			if contains(t.Requirement, term) && inHead(head, term) {
				n++
				if t.Must {
					n++
				}
				break
			}
		}
	}
	return n
}

// inHead finds a term in the head of a line. A phrase may be split by the
// words that tell it what it is about ("event-driven Java and Spring Boot
// microservices" still opens the line with event-driven microservices), so a
// multi-word term also matches when each of its words is there.
func inHead(head, term string) bool {
	if contains(head, term) {
		return true
	}
	tw, hw := words(term), words(head)
	if len(tw) < 2 {
		return false
	}
	for w := range tw {
		if !hw[w] {
			return false
		}
	}
	return true
}

// parity counts the posting spellings the draft gains for entities the source
// line already names ("PostgreSQL" where the line said "Postgres"), which an
// ATS that matches strings rewards and which claims nothing new. An approved
// framing of this line counts the same way: its facts already say it, the
// posting asks for it in those words, and the first E3 runs showed the cost of
// ignoring it. "10 Java and Spring Boot services using a microservices
// architecture" has the posting's word past the tenth, so it earned no
// evidence, tied the original and lost.
//
// Two terms at most. A line that gains a third is stuffing, which T4 forbids.
func parity(plan Plan, original, text string, s Scope) int {
	l := &ledger.Ledger{Entities: s.Forms}
	n := 0
	for _, term := range plan.AllowedTerms {
		if contains(text, term) && !contains(original, term) &&
			(namesEntity(plan, original, formsOf(l, term)) || framed(plan, term)) {
			n++
		}
	}
	return min(n, maxParity)
}

const maxParity = 2

func framed(plan Plan, term string) bool {
	return slices.ContainsFunc(plan.Bullet.Framings, func(f ledger.Framing) bool {
		return f.Approved && !f.Rejected && strings.EqualFold(f.Phrase, term)
	})
}

func namesEntity(plan Plan, original string, forms []string) bool {
	return slices.ContainsFunc(forms, func(f string) bool {
		return contains(original, f) || slices.ContainsFunc(plan.Bullet.Facts, func(fa ledger.Fact) bool {
			return slices.ContainsFunc(fa.Entities, func(e string) bool { return strings.EqualFold(e, f) })
		})
	})
}
