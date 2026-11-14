// Package suggest proposes ATS-minded rewordings of a finished, tailored CV.
//
// The tailored text comes first and is complete without any of this: Phase 3
// lifts bullets and skills out of cv.md and ships them. A suggestion is a second
// opinion on one of those strings, made against the posting, that the person
// accepts or rejects. Nothing here touches the PDF; accepted items are applied
// to the content when a rebuild is asked for.
//
// The model sees numbered strings and answers with numbers, so the original of
// every suggestion is the string that is on the page and never the model's
// copy of it. What it writes is then vetted in code, because a reword is the one
// place Phase 3 can now say something cv.md does not.
package suggest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/guards"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

const (
	Bullet = "bullet"
	Skill  = "skill"

	Accepted = "accepted"
	Rejected = "rejected"

	// File sits beside cv-content.json in the offer's output folder.
	File = "suggestions.json"

	// AdaptPrefix starts the id of an item that offers to put the CV's own line
	// back where `--writer adapt` changed it. Generating suggestions keeps these.
	AdaptPrefix = "adapt-"
)

// Item is one suggestion. Old is the string as it stands on the page.
type Item struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Where string `json:"where"` // the company, project or skills category it sits under
	Old   string `json:"old"`
	New   string `json:"new"`
	Why   string `json:"why"`
	State string `json:"state,omitempty"` // "", Accepted or Rejected
}

// Target is one string of the tailored text a suggestion can be about.
type Target struct {
	ID    string
	Kind  string
	Where string
	Text  string
}

// Targets lists the bullets and skill items the content would print, in page
// order. The ids are positional, so the same content always numbers the same.
func Targets(c fill.Content) []Target {
	var out []Target
	add := func(kind, prefix, where, s string) {
		n := 0
		for _, t := range out {
			if t.Kind == kind {
				n++
			}
		}
		out = append(out, Target{ID: prefix + strconv.Itoa(n+1), Kind: kind, Where: where, Text: s})
	}
	for _, e := range c.Experience {
		for _, b := range e.Bullets {
			add(Bullet, "B", e.Company, b)
		}
	}
	if picks, ok := c.ProjectPicks(); ok {
		for _, p := range picks {
			for _, b := range p.Bullets {
				add(Bullet, "B", p.Name, b)
			}
		}
	}
	for _, r := range c.SkillRows() {
		for _, it := range fill.SkillItems(r.Items) {
			add(Skill, "K", r.Category, it)
		}
	}
	return out
}

// Input is everything one generation needs.
type Input struct {
	Content fill.Content
	CVText  string
	JD      string
	System  string // prompts/p3-suggest.md
}

type proposal struct {
	ID  string `json:"id"`
	New string `json:"new"`
	Why string `json:"why"`
}

func capped(n int) ollama.Schema {
	return ollama.Obj(ollama.F("type", "string"), ollama.F("maxLength", n))
}

func schema() ollama.Schema {
	item := ollama.Obj(
		ollama.F("type", "object"),
		ollama.F("properties", ollama.Obj(
			ollama.F("id", capped(8)),
			ollama.F("new", capped(400)),
			ollama.F("why", capped(240)),
		)),
		ollama.F("required", []string{"id", "new", "why"}),
	)
	return ollama.Obj(
		ollama.F("type", "object"),
		ollama.F("properties", ollama.Obj(
			ollama.F("suggestions", ollama.Obj(
				ollama.F("type", "array"), ollama.F("maxItems", 12), ollama.F("items", item))),
		)),
		ollama.F("required", []string{"suggestions"}),
	)
}

// Generate asks the model for rewordings and returns the ones that pass Vet.
func Generate(ctx context.Context, ol *ollama.Client, id int, in Input) ([]Item, error) {
	targets := Targets(in.Content)
	if len(targets) == 0 {
		return nil, errors.New("the tailored CV has no bullets or skills to review")
	}
	var b strings.Builder
	b.WriteString("## Job description\n" + text.CleanJD(in.JD, 4000) + "\n\n## Bullets\n")
	where := ""
	for _, t := range targets {
		if t.Kind != Bullet {
			continue
		}
		if t.Where != where {
			where = t.Where
			fmt.Fprintf(&b, "[%s]\n", where)
		}
		fmt.Fprintf(&b, "%s: %s\n", t.ID, t.Text)
	}
	b.WriteString("\n## Skills\n")
	where = ""
	for _, t := range targets {
		if t.Kind != Skill {
			continue
		}
		if t.Where != where {
			where = t.Where
			fmt.Fprintf(&b, "[%s]\n", where)
		}
		fmt.Fprintf(&b, "%s: %s\n", t.ID, t.Text)
	}

	var out struct {
		Suggestions []proposal `json:"suggestions"`
	}
	if err := ol.Chat(ctx, ollama.Request{
		Model: ollama.ModelEval, System: in.System, User: b.String(),
		Schema:  schema(),
		Options: ollama.Options{Temperature: 0, NumCtx: 8192, NumPredict: 2400},
		Timeout: 300 * time.Second, Label: "p3.suggest", ID: id,
	}, &out); err != nil {
		return nil, err
	}
	return vet(targets, out.Suggestions, in.CVText, in.JD), nil
}

var nonWord = regexp.MustCompile(`[^a-z0-9+#]+`)

// norm lowercases and spaces, padded so a phrase cannot straddle a word.
func norm(s string) string {
	return " " + strings.TrimSpace(nonWord.ReplaceAllString(strings.ToLower(s), " ")) + " "
}

// vet keeps the proposals that are safe to show.
//
// A bullet keeps every figure it had and adds none, names no product cv.md does
// not, swaps at most maxNewWords words and grows by at most an eighth (the page
// is a line budget, and a longer reword is a new claim: the model's habit is to
// append an outcome the CV never states), and brings in at least one word the
// posting uses that it lacked — otherwise it is not an ATS suggestion, just a
// different sentence. A skill must be a spelling
// the posting itself uses, one item, and pass the same product check.
//
// The product lists are a floor, not a proof: this narrows what a person has to
// read, and the person still decides.
func vet(targets []Target, props []proposal, cvText, jd string) []Item {
	byID := map[string]Target{}
	for _, t := range targets {
		byID[t.ID] = t
	}
	jdTok, jdNorm := text.TokenSet(jd), norm(jd)
	seen := map[string]bool{}
	var out []Item
	for _, p := range props {
		t, ok := byID[strings.TrimSpace(p.ID)]
		n := strings.TrimSpace(p.New)
		if !ok || seen[t.ID] || n == "" || n == t.Text || strings.ContainsAny(n, "\r\n") {
			continue
		}
		if len(guards.ProductFab(n, cvText)) > 0 || len(guards.CasedFab(n, cvText)) > 0 {
			continue
		}
		switch t.Kind {
		case Bullet:
			// Containing the original whole means words were only added around
			// it, which is a new claim and not a respelling of the old one.
			if strings.Contains(n, strings.TrimRight(t.Text, ". ")) || len(n) > len(t.Text)*9/8+10 || !keepsFigures(t.Text, n) || !bringsJDWord(t.Text, n, jdTok) {
				continue
			}
		case Skill:
			if strings.Contains(n, ",") || len(n) > 60 || !strings.Contains(jdNorm, norm(n)) {
				continue
			}
		}
		seen[t.ID] = true
		out = append(out, Item{ID: t.ID, Kind: t.Kind, Where: t.Where, Old: t.Text, New: n, Why: strings.TrimSpace(p.Why)})
	}
	return out
}

func keepsFigures(old, new string) bool {
	have, had := guards.NumbersIn(new), guards.NumbersIn(old)
	for n := range have {
		if !had[n] {
			return false
		}
	}
	for n := range had {
		if !have[n] {
			return false
		}
	}
	return true
}

// maxNewWords bounds how much a bullet may change. "REST services" to "RESTful
// API services" is two; a clause tacked on the end is four or more.
const maxNewWords = 3

// bringsJDWord reports whether new adds a word from the posting, and no more
// than maxNewWords words of any kind.
func bringsJDWord(old, new string, jd map[string]bool) bool {
	was := text.TokenSet(old)
	added, fromJD := 0, false
	for w := range text.TokenSet(new) {
		if !was[w] {
			added++
			fromJD = fromJD || jd[w]
		}
	}
	return fromJD && added <= maxNewWords
}

// Apply returns the content with every accepted item swapped in, and how many
// landed. An item whose original is no longer on the page is skipped: the CV
// was re-tailored since, and the suggestion was about text that is gone.
func Apply(c fill.Content, items []Item) (fill.Content, int) {
	n := 0
	for _, it := range items {
		if it.State != Accepted {
			continue
		}
		var ok bool
		if it.Kind == Skill {
			c, ok = c.ReplaceSkill(it.Old, it.New)
		} else {
			c, ok = c.ReplaceBullet(it.Old, it.New)
		}
		if ok {
			n++
		}
	}
	return c, n
}

// Approved is the accepted skill spellings, which the renderer must be told are
// grounded: they are in the posting, not in cv.md.
func Approved(items []Item) []string {
	var out []string
	for _, it := range items {
		if it.Kind == Skill && it.State == Accepted {
			out = append(out, it.New)
		}
	}
	return out
}

// Load reads the offer folder's suggestions. A folder with none gives nil.
func Load(dir string) ([]Item, error) {
	b, err := os.ReadFile(filepath.Join(dir, File))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var items []Item
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, fmt.Errorf("%s: %w", File, err)
	}
	return items, nil
}

func Save(dir string, items []Item) error {
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return state.WriteAtomic(filepath.Join(dir, File), b)
}

// Mark records a decision. The file is re-read first, so two screens or a
// regeneration landing in between cannot be overwritten with a stale list.
func Mark(dir, id, st string) error {
	items, err := Load(dir)
	if err != nil {
		return err
	}
	for i := range items {
		if items[i].ID == id {
			items[i].State = st
			return Save(dir, items)
		}
	}
	return fmt.Errorf("no suggestion %s", id)
}
