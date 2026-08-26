// Package embed is the semantic retrieval layer: CV atoms, a past-JD
// calibration index, and brute-force cosine over both.
//
// Brute force is the right shape at this corpus size — around a hundred CV
// atoms and a few hundred JDs. A vector database is pure overhead here; revisit
// past roughly 50k vectors.
package embed

import (
	"os"
	"regexp"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/text"
)

// Atom is one retrievable piece of the CV.
type Atom struct {
	Text   string    `json:"text"`
	Source string    `json:"source"`
	Vec    []float64 `json:"vec,omitempty"`
}

var cvSections = map[string]bool{
	"Summary": true, "Experience": true, "Projects": true, "Education": true,
	"Certifications": true, "Skills": true, "Languages": true,
}

var (
	entityH   = regexp.MustCompile(`^###\s+(.+?)\s*$`)
	sectionH  = regexp.MustCompile(`^##\s+(.+?)\s*$`)
	boldAlone = regexp.MustCompile(`^\*\*([^*]+)\*\*$`)
	locationH = regexp.MustCompile(`(?i)\*\*Location:?\*\*:?\s*([^|]+)`)
	labelled  = regexp.MustCompile(`^\*\*([^*:]+):?\*\*:?\s*(.+)$`)
)

// ExtractAtoms splits the CV into retrievable atoms.
//
// Each atom carries an entity prefix ("Acme Corp — Software Engineer: <bullet>")
// so a bullet stays meaningful out of context, which is what it will be when it
// comes back from a similarity search.
func ExtractAtoms(cvMD, digestMD string) []Atom {
	var atoms []Atom
	cv := text.CleanCV(cvMD)

	entity, section := "", ""
	for _, raw := range strings.Split(cv, "\n") {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		// Entity header first: "## " needs whitespace, so there is no overlap.
		if m := entityH.FindStringSubmatch(s); m != nil {
			entity = m[1]
			continue
		}
		m := sectionH.FindStringSubmatch(s)
		if m == nil {
			// Legacy spelling, where a bold-alone line that is not a section
			// name is an entity.
			m = boldAlone.FindStringSubmatch(s)
		}
		if m != nil {
			name := strings.TrimSpace(m[1])
			if cvSections[name] {
				section, entity = name, ""
			} else {
				entity = name
			}
			continue
		}

		// The one contact-header field worth retrieving. JDs make "currently
		// based in <country>" a MUST, and with no location atom in the index
		// that row could only ever grade Gap — it did, twice, in real reports.
		// Phrased to match the JD's wording rather than the CV's, so cosine
		// similarity actually lands.
		if section == "" {
			if loc := locationH.FindStringSubmatch(s); loc != nil {
				atoms = append(atoms, Atom{Text: "Currently based in " + strings.TrimSpace(loc[1]), Source: "contact"})
				continue
			}
		}

		// "**Category:** items" — skills rows, Key Modules in Education. Gated
		// by section so a contact-header line never becomes an atom.
		if lm := labelled.FindStringSubmatch(s); lm != nil && (section == "Skills" || section == "Education") {
			atoms = append(atoms, Atom{
				Text:   section + " — " + strings.TrimSpace(lm[1]) + ": " + strings.TrimSpace(lm[2]),
				Source: strings.ToLower(section),
			})
			continue
		}

		// "**Org/Type** | tech, tech | dates" under an entity. The tech stack is
		// prime retrieval material, as is a degree line.
		if (section == "Projects" || section == "Education") && strings.HasPrefix(s, "**") && strings.Contains(s, "|") {
			meta := strings.TrimSpace(strings.ReplaceAll(s, "**", ""))
			if entity != "" {
				meta = entity + " — " + meta
			}
			atoms = append(atoms, Atom{Text: meta, Source: strings.ToLower(section)})
			continue
		}

		if strings.HasPrefix(s, "- ") {
			prefix := ""
			if entity != "" {
				prefix = entity + ": "
			}
			src := strings.ToLower(section)
			if src == "" {
				src = "cv"
			}
			atoms = append(atoms, Atom{Text: prefix + strings.TrimSpace(s[2:]), Source: src})
			continue
		}

		if section == "Summary" && len(s) > 60 {
			for _, sent := range text.SplitSentences(s) {
				if len(strings.TrimSpace(sent)) > 40 {
					atoms = append(atoms, Atom{Text: strings.TrimSpace(sent), Source: "summary"})
				}
			}
		}
	}

	for _, raw := range strings.Split(digestMD, "\n") {
		s := strings.TrimSpace(raw)
		if strings.HasPrefix(s, "- ") && len(s) > 30 {
			atoms = append(atoms, Atom{Text: strings.TrimSpace(s[2:]), Source: "article-digest"})
		}
	}
	return atoms
}

// ReadFileOr returns a file's contents, or "" when it is absent. article-digest
// is optional.
func ReadFileOr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
