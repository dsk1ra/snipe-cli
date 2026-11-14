package phase2

import (
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
)

// FactAtoms replaces each CV bullet atom with one atom per ledger fact of that
// bullet (PRD §5.2, stage 2 over the ledger). A fact is a shorter unit than a
// bullet, so a requirement matches the clause that answers it instead of a long
// line it shares one word with, and the fact stays tied to its entry through the
// entry prefix the bullet atom carried. Parent keeps the bullet's text.
//
// Atoms that are not a bullet the ledger knows (the summary, the location line,
// the skills and education catalogue) pass through unchanged, and so does a
// bullet whose facts were all rejected.
func FactAtoms(atoms []embed.Atom, led *ledger.Ledger) []embed.Atom {
	if led == nil {
		return atoms
	}
	var out []embed.Atom
	for _, a := range atoms {
		b, prefix := ledgerBullet(a.Text, led)
		if b == nil {
			out = append(out, a)
			continue
		}
		n := 0
		for _, f := range b.Facts {
			if f.Status == "rejected" || strings.TrimSpace(f.Text) == "" {
				continue
			}
			out = append(out, embed.Atom{Text: prefix + f.Text, Source: a.Source, Parent: a.Text})
			n++
		}
		if n == 0 {
			out = append(out, a)
		}
	}
	return out
}

// ledgerBullet finds the bullet an atom was cut from and the entry prefix in
// front of it. The atom is the prefix plus the bullet's text.
func ledgerBullet(atom string, led *ledger.Ledger) (*ledger.Bullet, string) {
	for i := range led.Entries {
		for j := range led.Entries[i].Bullets {
			b := &led.Entries[i].Bullets[j]
			if b.Text != "" && strings.HasSuffix(atom, b.Text) {
				return b, strings.TrimSuffix(atom, b.Text)
			}
		}
	}
	return nil, ""
}
