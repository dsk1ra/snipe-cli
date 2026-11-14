// Package ledger is the fact ledger of docs/PRD-TAILORING-REWORK.md §6.1: every
// claim cv.md makes, entry by entry, plus the generalisations the user has
// approved. It is the only place the pipeline learns what the user can claim.
package ledger

// Ledger is batch/state/cv-ledger.json. It carries approvals, so it lives in
// state/ and is never rebuilt from scratch: Build merges into the old one.
type Ledger struct {
	CVHash   string              `json:"cv_hash"`
	Entries  []Entry             `json:"entries"`
	Entities map[string][]string `json:"entities"` // canonical name → surface forms
}

type Entry struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"` // "experience" | "project"
	Title      string   `json:"title"`
	Qualifiers []string `json:"qualifiers,omitempty"` // "Supervised Project"
	Bullets    []Bullet `json:"bullets"`
}

type Bullet struct {
	Hash     string    `json:"hash"` // sha1 of Text; the key approvals hang on
	Text     string    `json:"text"`
	Render   *bool     `json:"render,omitempty"` // nil = true; false for article-digest lines
	Facts    []Fact    `json:"facts"`
	Framings []Framing `json:"framings,omitempty"`
	// FramedAt is the day the framings pass last looked at this exact text. It
	// is what makes the pass skip a bullet that came out with no proposals; the
	// hash already resets it when the bullet is edited.
	FramedAt string `json:"framed_at,omitempty"`
}

type Fact struct {
	ID       string   `json:"id"` // "<entry>/<n><letter>"
	Text     string   `json:"text"`
	Entities []string `json:"entities,omitempty"`
	Figures  []string `json:"figures,omitempty"`
	MustKeep bool     `json:"must_keep,omitempty"`
	Source   string   `json:"source,omitempty"`   // "" = cv.md, "inferred" = approved inference
	Approved string   `json:"approved,omitempty"` // "<posting>@<date>" for inferred facts
	Status   string   `json:"status,omitempty"`   // "" = pending review, "approved", "rejected"
}

type Framing struct {
	Phrase   string   `json:"phrase"`
	From     []string `json:"from"` // fact ids
	Approved bool     `json:"approved"`
	Rejected bool     `json:"rejected,omitempty"`
}
