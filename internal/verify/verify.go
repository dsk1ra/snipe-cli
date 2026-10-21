// Package verify is the verification pipeline of docs/PRD-TAILORING-REWORK.md
// §6.4: a rewritten bullet is checked against the ledger by code first, then by
// a fact-checking model in both directions, and anything that fails any step is
// dropped. The original bullet then ships instead, so a false negative costs
// only adaptation and a false positive costs a false line on the page.
package verify

import (
	"context"
	"fmt"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/ledger"
)

// Checker is MiniCheck's contract (internal/minicheck.Client satisfies it):
// does doc support claim. It is an interface so tests need no model.
type Checker interface {
	Check(ctx context.Context, doc, claim string) (bool, error)
}

type Class string

const (
	Supported Class = "supported" // every claim rests on cv.md facts and approved framings
	Inferred  Class = "inferred"  // supported only with the entry's approved inferences
	Dropped   Class = "dropped"
)

type Result struct {
	Class  Class
	Reason string // why it was dropped; empty otherwise
}

// Options are the choices E2 (§8.2) has not settled.
type Options struct {
	// Split turns a draft into the claims sent to the model; every claim must
	// be supported. Nil means the whole bullet is one claim. MiniCheck's card
	// asks for single sentences, but whether one check catches a bad clause in
	// a long bullet is exactly what E2 measures, so ByClause is one switch away.
	Split func(string) []string

	// Entities is ledger.Ledger.Entities: canonical name to surface forms. A
	// fact records "Postgres"; the draft may say "PostgreSQL". Without it only
	// the names themselves count.
	Entities map[string][]string

	// MustKeepOnly limits preservation to must_keep facts, which is all PRD
	// §6.4 asks for. The default checks every fact: the first E3 run shipped a
	// services line that lost "covering orders, stock, pricing, delivery,
	// loyalty and finance", true and weaker, because that clause was not
	// must_keep. A rewrite re-orders and re-words; it does not get to delete.
	MustKeepOnly bool
}

func (o Options) split(s string) []string {
	if o.Split == nil {
		return []string{s}
	}
	if cs := o.Split(s); len(cs) > 0 {
		return cs
	}
	return []string{s}
}

// Verify runs the §6.4 chain on one draft of one bullet: code checks, then
// attribution, then preservation. allowedTerms are extra names the caller has
// vouched for (stage 1's typed posting list). A checker error is returned with
// a Dropped result; an unanswered question is never a pass.
func Verify(ctx context.Context, c Checker, e ledger.Entry, b ledger.Bullet, draft string, allowedTerms []string, opts ...Options) (Result, error) {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	if r := Code(e, b, draft, allowedTerms, o); r != "" {
		return Result{Dropped, r}, nil
	}

	doc := document(b)
	ok, err := supports(ctx, c, doc, o.split(draft))
	if err != nil {
		return Result{Dropped, "attribution: " + err.Error()}, err
	}
	class := Supported
	if !ok {
		// Second chance with the inferences the user approved for this entry.
		// Skipped when there are none: the same document would fail again.
		inf := approvedInferences(e)
		if len(inf) == 0 {
			return Result{Dropped, "attribution: not supported by the entry's facts"}, nil
		}
		ok, err = supports(ctx, c, doc+"\n"+strings.Join(inf, "\n"), o.split(draft))
		if err != nil {
			return Result{Dropped, "attribution: " + err.Error()}, err
		}
		if !ok {
			return Result{Dropped, "attribution: not supported even with approved inferences"}, nil
		}
		class = Inferred
	}

	// Preservation: attribution cannot see what a draft left out, and dropping
	// "simulated" or "Supervised" inflates the claim without adding a word.
	for _, f := range b.Facts {
		if (o.MustKeepOnly && !f.MustKeep) || !usable(f) {
			continue
		}
		ok, err := c.Check(ctx, draft, f.Text)
		if err != nil {
			return Result{Dropped, "preservation: " + err.Error()}, err
		}
		if !ok {
			return Result{Dropped, fmt.Sprintf("preservation: drops %s %q", f.ID, f.Text)}, nil
		}
	}
	return Result{Class: class}, nil
}

// Code runs the four code checks in §6.4 order and returns the first reason.
func Code(e ledger.Entry, b ledger.Bullet, draft string, allowedTerms []string, o Options) string {
	if r := Figures(b.Text, draft); r != "" {
		return "figures: " + r
	}
	if r := Qualifiers(b.Text, draft); r != "" {
		return "qualifiers: " + r
	}
	if r := Scope(b.Text, draft, allowedTerms); r != "" {
		return "scope: " + r
	}
	if r := Stuffing(b.Text, draft, allowedTerms, o.Entities); r != "" {
		return "stuffing: " + r
	}
	if r := EntityClosure(b.Text, draft, forms(e, o.Entities, allowedTerms)); r != "" {
		return "entities: " + r
	}
	if r := LineCost(b.Text, draft); r != "" {
		return "line cost: " + r
	}
	if r := Shape(b.Text, draft); r != "" {
		return "shape: " + r
	}
	return ""
}

func supports(ctx context.Context, c Checker, doc string, claims []string) (bool, error) {
	for _, cl := range claims {
		ok, err := c.Check(ctx, doc, cl)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// usable is a fact the pipeline may lean on: rejected facts never, and an
// inference only once the user has approved it. A cv.md fact still pending
// review is cv.md's own wording, so it stands.
func usable(f ledger.Fact) bool {
	return f.Status != "rejected" && (f.Source != "inferred" || f.Status == "approved")
}

// document is what attribution checks a draft against: this bullet's cv.md
// facts and the framings the user approved. The bullet's own text stands in
// when the ledger has not decomposed it yet.
func document(b ledger.Bullet) string {
	var parts []string
	for _, f := range b.Facts {
		if f.Source != "inferred" && usable(f) {
			parts = append(parts, f.Text)
		}
	}
	if len(parts) == 0 {
		parts = append(parts, b.Text)
	}
	for _, fr := range b.Framings {
		if fr.Approved && !fr.Rejected {
			parts = append(parts, fr.Phrase)
		}
	}
	return strings.Join(parts, "\n")
}

func approvedInferences(e ledger.Entry) []string {
	var out []string
	for _, b := range e.Bullets {
		for _, f := range b.Facts {
			if f.Source == "inferred" && usable(f) {
				out = append(out, f.Text)
			}
		}
	}
	return out
}

// forms is every lowercased name the entry may use: the entities its usable
// facts record (any bullet of the entry, inferences included once approved),
// their surface forms from the ledger, the entry's own title and qualifiers,
// and the caller's allowed terms. Multi-word forms also enter word by word,
// because the closure check looks at one word at a time.
func forms(e ledger.Entry, entities map[string][]string, allowed []string) map[string]bool {
	byName := map[string][]string{}
	for k, v := range entities {
		byName[strings.ToLower(k)] = v
	}
	out := map[string]bool{}
	add := func(s string) {
		out[strings.ToLower(strings.TrimSpace(s))] = true
		for _, w := range words(s) {
			out[strings.ToLower(w)] = true
		}
	}
	for _, s := range allowed {
		add(s)
	}
	add(e.Title)
	for _, q := range e.Qualifiers {
		add(q)
	}
	for _, b := range e.Bullets {
		for _, f := range b.Facts {
			if !usable(f) {
				continue
			}
			for _, name := range f.Entities {
				add(name)
				for _, s := range byName[strings.ToLower(name)] {
					add(s)
				}
			}
		}
	}
	return out
}

// Known is the set of names EntityClosure treats as recorded for this entry
// (see forms), exported so the gate can recheck a shipped line independently.
func Known(e ledger.Entry, entities map[string][]string, allowed []string) map[string]bool {
	return forms(e, entities, allowed)
}

// ByClause is a Split for Options: it cuts at semicolons, dashes and commas
// and folds fragments of under three words into their neighbour, since a
// claim like "reducing latency" with no subject gives the model nothing to
// judge. It is a starting point for E2, not a parser.
func ByClause(s string) []string {
	cut := strings.NewReplacer("; ", "\x00", " — ", "\x00", " – ", "\x00", ", ", "\x00")
	var out []string
	for _, f := range strings.Split(cut.Replace(s), "\x00") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if n := len(out); n > 0 && (len(strings.Fields(f)) < 3 || len(strings.Fields(out[n-1])) < 3) {
			out[n-1] += ", " + f
			continue
		}
		out = append(out, f)
	}
	return out
}
