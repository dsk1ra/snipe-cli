package ledger

import (
	"slices"
	"strings"
	"unicode"
)

// verify keeps what the bullet backs up and drops the rest.
//
// The ledger is the only place the pipeline learns what can be claimed, so a
// fact the model made up would later license a rewrite. Three checks, all in
// code: a listed figure must appear in the bullet; an entity must appear in
// the bullet or the entry title (case-insensitive), which stops the model
// importing "Kubernetes" from its own idea of the stack; and every digit-bearing
// token of the bullet must sit in some fact's figures, so a decomposition that
// loses a number cannot pass. A fact failing the first two is dropped whole. A
// missing figure is added to the first fact rather than rejecting the lot,
// since the loss is the model's omission, not a fabrication.
//
// A bullet left with no facts becomes one must_keep fact holding its own text:
// verbatim and unsplit is always safe, only less useful.
func verify(bullet, title string, qualifiers []string, in []rawFact) []Fact {
	low, tlow := strings.ToLower(bullet), strings.ToLower(title)
	var out []Fact
	seen := map[string]bool{}
next:
	for _, r := range in {
		f := Fact{Text: strings.TrimSpace(r.Text), MustKeep: r.MustKeep}
		if f.Text == "" || seen[strings.ToLower(f.Text)] {
			continue
		}
		for _, fig := range clean(r.Figures) {
			if !strings.Contains(low, strings.ToLower(fig)) {
				continue next
			}
			f.Figures = append(f.Figures, fig)
		}
		for _, e := range clean(r.Entities) {
			if l := strings.ToLower(e); !strings.Contains(low, l) && !strings.Contains(tlow, l) {
				continue next
			}
			f.Entities = append(f.Entities, e)
		}
		seen[strings.ToLower(f.Text)] = true
		out = append(out, f)
	}
	if len(out) == 0 {
		out = []Fact{{Text: strings.TrimSpace(bullet), MustKeep: true}}
	}
	for _, tok := range digitTokens(bullet) {
		if !covered(tok, out) {
			out[0].Figures = append(out[0].Figures, tok)
		}
	}
	for i := range out {
		if out[i].Text != strings.TrimSpace(bullet) { // the fallback stays must_keep
			Tidy(&out[i], qualifiers)
		}
	}
	return out
}

// Hedges are the words that, if a rewrite drops them, inflate the claim
// (Takano's qualifier category): "simulated", "Supervised Project", "working
// knowledge". They are fixed here because the model does not mark them.
var Hedges = []string{"simulated", "supervised", "prototype", "basic", "working knowledge", "distinction", "honours"}

// Tidy normalises what the model wrote into a fact. The first ledger build
// marked 139 of 144 facts must_keep and listed "12,000 orders/second", "12,000"
// and "k6" as separate figures, which would have made the preservation check
// (PRD §6.4) run on nearly every fact and fail on spelling. So figures become
// the digit-bearing words of what the model listed, deduplicated, and must_keep
// is code's call: a fact with a figure, or one saying a hedge or the entry's
// qualifier. The reviewer can still toggle it for an outcome fact.
func Tidy(f *Fact, qualifiers []string) {
	var figs []string
	for _, fig := range f.Figures {
		for _, tok := range digitTokens(fig) {
			if !slices.ContainsFunc(figs, func(x string) bool { return key(x) == key(tok) }) {
				figs = append(figs, tok)
			}
		}
	}
	f.Figures = figs
	low := strings.ToLower(f.Text)
	f.MustKeep = len(figs) > 0
	for _, h := range append(slices.Clone(Hedges), qualifiers...) {
		if h != "" && strings.Contains(low, strings.ToLower(h)) {
			f.MustKeep = true
		}
	}
}

func clean(ss []string) []string {
	var out []string
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// digitTokens are the whitespace-separated words of s that contain a digit,
// edge punctuation trimmed: "£85K+", "99.97%", "AES-256-GCM", "1080p60". Single
// digits count: "6-step saga" and "Spring Boot 3" are claims here, where
// guards.NumbersIn skips them as noise.
func digitTokens(s string) []string {
	var out []string
	for _, w := range strings.Fields(s) {
		w = strings.TrimFunc(w, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("£$€+%", r)
		})
		w = strings.TrimRight(w, "+")
		if strings.IndexFunc(w, unicode.IsDigit) >= 0 && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

func key(s string) string { return strings.ToLower(strings.Trim(s, "£$€+%.,")) }

// covered reports whether tok is a listed figure, or splits at -, – or / into
// digit pieces that each are ("3-4" by "3" and "4"). Equality, never substring:
// "10" is inside "1080p60" and must not vouch for it.
func covered(tok string, facts []Fact) bool {
	has := func(s string) bool {
		for _, f := range facts {
			for _, fig := range f.Figures {
				if key(fig) == key(s) {
					return true
				}
			}
		}
		return false
	}
	if has(tok) {
		return true
	}
	pieces := strings.FieldsFunc(tok, func(r rune) bool { return r == '-' || r == '–' || r == '/' })
	if len(pieces) < 2 {
		return false
	}
	for _, p := range pieces {
		if strings.IndexFunc(p, unicode.IsDigit) >= 0 && !has(p) {
			return false
		}
	}
	return true
}
