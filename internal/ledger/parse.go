package ledger

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
)

// Hash is the key approvals hang on. sha1 of the exact bullet text: a one-word
// edit is a new bullet with new facts, which is the point (PRD §6.1).
func Hash(text string) string {
	h := sha1.Sum([]byte(text))
	return hex.EncodeToString(h[:])
}

// CVHash covers both inputs, so --check can say "nothing changed" without
// parsing either.
func CVHash(cv, digest string) string { return Hash(cv + "\x00" + digest) }

var (
	boldRe    = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	slugRe    = regexp.MustCompile(`[^a-z0-9]+`)
	repoRe    = regexp.MustCompile(`github\.com/[^/\s]+/([A-Za-z0-9._-]+)`)
	companyRe = regexp.MustCompile(`^\*\*([^*]+)\*\*\s*(?:—|–|-)\s`)
)

func slug(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func plain(s string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.ReplaceAll(s, "**", ""), "### "))
}

// DigestID is the one entry article-digest.md becomes.
const DigestID = "article-digest"

// Parse turns cv.md and article-digest.md into entries whose bullets carry a
// hash and text and no facts yet.
//
// It reuses cvselect's section and entry parsing rather than a parser of its
// own: the selector and the writer slice cv.md with those functions, so a
// ledger bullet is by construction the same string they ship, wrapped lines
// already joined. A second parser would drift, and a bullet the ledger can't
// match back to the page is a bullet no framing can attach to.
//
// Digest lines enter the way embed.ExtractAtoms reads them ("- " and over 30
// characters) and are flagged Render=false: evidence, never a printed line.
func Parse(cvText, digestText string) []Entry {
	var out []Entry
	seen := map[string]int{}
	for _, sec := range cvselect.ParseSections(cvText) {
		kind := map[string]string{"Experience": "experience", "Projects": "project"}[sec.Name]
		if kind == "" {
			continue
		}
		for _, e := range cvselect.ParseEntries(sec.Lines).Entries {
			ent := Entry{Kind: kind, Title: plain(e.Head[0])}
			id := ""
			for _, h := range e.Head[1:] {
				h = strings.TrimSpace(h)
				switch {
				case kind == "experience" && companyRe.MatchString(h):
					c := plain(companyRe.FindStringSubmatch(h)[1])
					ent.Title += " - " + c
					id = slug(c)
				case kind == "project" && strings.HasPrefix(h, "**") && strings.Contains(h, "|"):
					ent.Qualifiers = []string{plain(strings.SplitN(h, "|", 2)[0])}
				case kind == "project" && repoRe.MatchString(h):
					id = slug(repoRe.FindStringSubmatch(h)[1])
				}
			}
			if id == "" {
				id = slug(ent.Title)
			}
			// Two roles at one employer would share an id, and ids key every
			// approval; the suffix keeps them apart in cv.md order.
			seen[id]++
			if seen[id] > 1 {
				id += "-" + string(rune('0'+seen[id]))
			}
			ent.ID = id
			for _, b := range e.Bullets {
				ent.Bullets = append(ent.Bullets, Bullet{Hash: Hash(b), Text: b})
			}
			out = append(out, ent)
		}
	}
	no := false
	dg := Entry{ID: DigestID, Kind: "digest", Title: "article-digest.md"}
	for _, raw := range strings.Split(digestText, "\n") {
		s := strings.TrimSpace(raw)
		if strings.HasPrefix(s, "- ") && len(s) > 30 {
			t := strings.TrimSpace(s[2:])
			dg.Bullets = append(dg.Bullets, Bullet{Hash: Hash(t), Text: t, Render: &no})
		}
	}
	if len(dg.Bullets) > 0 {
		out = append(out, dg)
	}
	return out
}
