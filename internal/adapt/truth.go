package adapt

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/verify"
)

// Truth accounting is PRD §1.1 and §8.1 made countable. A claim is one fact of
// the ledger, so the 80/20 ratio needs no model call: the verifier already said
// whether a shipped line rests on facts alone or needed an approved inference,
// and the ledger says which facts the line came from.

// Claims is what one shipped bullet asserts, by class.
type Claims struct {
	Supported    int
	Inferred     int
	Unapproved   int
	Framings     []string // approved framings the shipped text uses and the original did not
	InferredUsed []string // ids of the approved inferred facts the text names
}

// claimsOf counts the claims of text as the plan's bullet ships it:
//
//   - original: every source fact, supported. The user wrote the line.
//   - Supported draft: every source fact too. Attribution passed against the
//     facts and approved framings, and preservation passed for the must-keeps,
//     so a changed line still says what the bullet said. A framing adds no
//     claim of its own: it is a name for facts already counted (its From ids are
//     the bullet's own facts).
//   - Inferred draft: those, plus one inferred claim per approved inferred fact
//     of the entry whose entity the text names. The verifier saw the inference
//     do work, so if no entity matches we still charge one: under-counting
//     inferences is the direction that breaks the contract.
//   - anything else: one unapproved claim, which Check turns into a failure.
//
// Rejected facts and the entry's inferred holding facts are not source facts.
func claimsOf(plan Plan, text string, class verify.Class, original string, s Scope) Claims {
	if class != verify.Supported && class != verify.Inferred {
		return Claims{Unapproved: 1}
	}
	var c Claims
	for _, f := range plan.Bullet.Facts {
		if f.Source != "inferred" && f.Status != "rejected" {
			c.Supported++
		}
	}
	if text == original {
		return c
	}
	for _, fr := range plan.Bullet.Framings {
		if fr.Approved && !fr.Rejected && contains(text, fr.Phrase) && !contains(original, fr.Phrase) {
			c.Framings = appendFold(c.Framings, fr.Phrase)
		}
	}
	if class != verify.Inferred {
		return c
	}
	l := &ledger.Ledger{Entities: s.Forms}
	for _, f := range s.Approved {
		named := slices.ContainsFunc(f.Entities, func(e string) bool {
			return slices.ContainsFunc(formsOf(l, e), func(form string) bool { return contains(text, form) })
		})
		if named {
			c.Inferred++
			c.InferredUsed = append(c.InferredUsed, f.ID)
		}
	}
	if c.Inferred == 0 {
		c.Inferred = 1
	}
	return c
}

// EntryTruth is one role's position (§8.1: supported_share is the minimum over
// roles, inferred_share the maximum). Entry is the ledger entry id.
type EntryTruth struct {
	Entry        string   `json:"entry"`
	Supported    int      `json:"supported"`
	Inferred     int      `json:"inferred"`
	Unapproved   int      `json:"unapproved"`
	Framings     []string `json:"framings,omitempty"`
	InferredUsed []string `json:"inferred_used,omitempty"`
}

func (e EntryTruth) total() int { return e.Supported + e.Inferred + e.Unapproved }

// SupportedShare is supported ÷ all claims. A role with no claims has nothing
// to violate, so it reads as fully supported.
func (e EntryTruth) SupportedShare() float64 {
	if e.total() == 0 {
		return 1
	}
	return float64(e.Supported) / float64(e.total())
}

// InferredShare is inferred ÷ all claims.
func (e EntryTruth) InferredShare() float64 {
	if e.total() == 0 {
		return 0
	}
	return float64(e.Inferred) / float64(e.total())
}

// MarshalJSON adds the two shares, so a reader outside Go (the batch metrics)
// need not redo the division. They are derived and ignored on the way back in.
func (e EntryTruth) MarshalJSON() ([]byte, error) {
	type plain EntryTruth // no methods, so no recursion
	return json.Marshal(struct {
		plain
		SupportedShare float64 `json:"supported_share"`
		InferredShare  float64 `json:"inferred_share"`
	}{plain(e), e.SupportedShare(), e.InferredShare()})
}

// Account sums the chosen bullets per entry, in order of first appearance.
// Bullets the ledger does not know (Entry "") carry no facts and are left out.
func Account(choices []Choice) []EntryTruth {
	var out []EntryTruth
	for _, c := range choices {
		if c.Entry == "" {
			continue
		}
		i := slices.IndexFunc(out, func(e EntryTruth) bool { return e.Entry == c.Entry })
		if i < 0 {
			out = append(out, EntryTruth{Entry: c.Entry})
			i = len(out) - 1
		}
		e := &out[i]
		e.Supported += c.Claims.Supported
		e.Inferred += c.Claims.Inferred
		e.Unapproved += c.Claims.Unapproved
		for _, f := range c.Claims.Framings {
			e.Framings = appendFold(e.Framings, f)
		}
		for _, id := range c.Claims.InferredUsed {
			if !slices.Contains(e.InferredUsed, id) {
				e.InferredUsed = append(e.InferredUsed, id)
			}
		}
	}
	return out
}

// Check is the kill rule: any role over its inferred budget, or with a claim
// that is neither supported nor approved, fails the offer's adaptation. The
// caller ships the verbatim lines instead.
func Check(entries []EntryTruth) error {
	var errs []error
	for _, e := range entries {
		if e.Unapproved > 0 {
			errs = append(errs, fmt.Errorf("%s: %d unapproved claim(s)", e.Entry, e.Unapproved))
		}
		if e.InferredShare() > MaxInferredShare+1e-9 {
			errs = append(errs, fmt.Errorf("%s: %.0f%% inferred, budget is %.0f%%",
				e.Entry, 100*e.InferredShare(), 100*MaxInferredShare))
		}
	}
	return errors.Join(errs...)
}

// ── truth.json ───────────────────────────────────────────────────────────────

// TruthName is the file's name inside an offer's output folder. The folder is
// output/<date>_<company>_<report>, built by phase3 where the PDF is written,
// and no paths.Layout method names it, so the caller joins the two.
const TruthName = "truth.json"

// TruthFile is truth.json. The TUI's diff review (§6.6) reads Bullets to show
// the original beside what shipped and to flag each changed line with what
// licensed it, and the batch metrics (§8.1) read Entries.
//
//	{
//	  "offer": "343",
//	  "entries": [{"entry": "shop", "supported": 9, "inferred": 1, "unapproved": 0,
//	               "framings": ["event-driven microservices"], "inferred_used": ["shop/9a"],
//	               "supported_share": 0.9, "inferred_share": 0.1}],
//	  "bullets": [{"entry": "shop", "hash": "<sha1 of the source bullet>", "source": "B",
//	               "kind": "framing", "class": "supported", "original": "...", "shipped": "...",
//	               "framings": ["event-driven microservices"], "why": "B: evidence 2, parity +0"}]
//	}
type TruthFile struct {
	Offer   string        `json:"offer"` // report number
	Entries []EntryTruth  `json:"entries"`
	Bullets []BulletTruth `json:"bullets"`
}

// BulletTruth is one selected bullet, changed or not. Source says which draft
// shipped ("original", "A", "B"); Kind says what licensed the change:
//
//	verbatim   the original, nothing to license
//	supported  a changed line resting on the bullet's facts alone
//	framing    a changed line that uses an approved framing's phrase
//	inference  a changed line that needed an approved inference (wins over framing)
type BulletTruth struct {
	Entry      string   `json:"entry,omitempty"`
	Hash       string   `json:"hash"`
	Source     string   `json:"source"`
	Kind       string   `json:"kind"`
	Class      string   `json:"class"`
	Original   string   `json:"original"`
	Shipped    string   `json:"shipped"`
	Framings   []string `json:"framings,omitempty"`
	Inferences []string `json:"inferences,omitempty"` // fact ids; the ledger has their text
	Why        string   `json:"why,omitempty"`
}

// NewTruthFile accounts for the choices and records what licensed each.
func NewTruthFile(offer string, choices []Choice) TruthFile {
	t := TruthFile{Offer: offer, Entries: Account(choices), Bullets: []BulletTruth{}}
	if t.Entries == nil {
		t.Entries = []EntryTruth{}
	}
	for _, c := range choices {
		kind := "supported"
		switch {
		case c.Source == "original":
			kind = "verbatim"
		case c.Claims.Inferred > 0:
			kind = "inference"
		case len(c.Claims.Framings) > 0:
			kind = "framing"
		}
		t.Bullets = append(t.Bullets, BulletTruth{
			Entry: c.Entry, Hash: c.Hash, Source: c.Source, Kind: kind, Class: string(c.Class),
			Original: c.Original, Shipped: c.Text,
			Framings: c.Claims.Framings, Inferences: c.Claims.InferredUsed, Why: c.Why,
		})
	}
	return t
}

// WriteTruth writes t to path, replacing any earlier file whole: a rebuild
// after an approval must not leave the TUI reading the old run's licences.
func WriteTruth(path string, t TruthFile) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".truth-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadTruth is the TUI's side. A missing file is the caller's to interpret:
// an offer built before adaptation has no truth.json.
func ReadTruth(path string) (TruthFile, error) {
	var t TruthFile
	b, err := os.ReadFile(path)
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return t, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}
