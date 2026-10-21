package ledger

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/ollama"
)

// InferredHash keys the per-entry holding bullet for approved inferred facts
// whose bullet is gone. An inference attaches to an entry, not to a line
// (PRD §1.1), so it must outlive the edit of the line it was first filed under.
const InferredHash = "inferred"

// Stats is what a build or a check found.
type Stats struct {
	Entries, Bullets, Facts int
	New, Reused, Dropped    int
	Calls                   int
}

// Diff compares cv.md and the digest with the ledger without calling the
// model, and says how many bullets a Build would send.
func Diff(cvText, digestText string, old *Ledger) Stats {
	var s Stats
	for _, e := range Parse(cvText, digestText) {
		s.Entries++
		known := map[string]bool{}
		if oe := findEntry(old, e.ID); oe != nil {
			for _, b := range oe.Bullets {
				known[b.Hash] = true
			}
		}
		fresh := 0
		for _, b := range e.Bullets {
			s.Bullets++
			if known[b.Hash] {
				s.Reused++
				delete(known, b.Hash)
			} else {
				s.New++
				fresh++
			}
		}
		for h := range known {
			if h != InferredHash {
				s.Dropped++
			}
		}
		if fresh > 0 {
			s.Calls += (fresh + chunk - 1) / chunk
		}
	}
	return s
}

func findEntry(l *Ledger, id string) *Entry {
	if l == nil {
		return nil
	}
	for i := range l.Entries {
		if l.Entries[i].ID == id {
			return &l.Entries[i]
		}
	}
	return nil
}

// Build merges cv.md and the digest into old and returns the new ledger.
//
// A bullet whose hash is already in old keeps its facts, framings and approvals
// untouched, and is never sent to the model: those carry hand decisions, and a
// 30B rerun at temperature 0 still shifts a fact boundary between model builds.
// Only new or edited bullets go out, one call per entry that has any. Bullets
// gone from cv.md drop with their framings; an approved inferred fact survives
// on its entry (see InferredHash), because the approval was about the entry.
//
// system is prompts/p3-ledger.md.
func Build(ctx context.Context, ol *ollama.Client, system, cvText, digestText string, old *Ledger) (*Ledger, Stats, error) {
	var st Stats
	out := &Ledger{CVHash: CVHash(cvText, digestText)}
	for _, e := range Parse(cvText, digestText) {
		oe := findEntry(old, e.ID)
		byHash := map[string]Bullet{}
		next := 0 // highest bullet number ever used in this entry
		var keep []Fact
		if oe != nil {
			for _, b := range oe.Bullets {
				byHash[b.Hash] = b
				for _, f := range b.Facts {
					next = max(next, factNum(f.ID))
				}
			}
		}
		var fresh []int // indexes into e.Bullets
		used := map[string]bool{}
		for i, b := range e.Bullets {
			if ob, ok := byHash[b.Hash]; ok && !used[b.Hash] {
				used[b.Hash] = true
				e.Bullets[i] = ob
				st.Reused++
			} else {
				fresh = append(fresh, i)
			}
		}
		if oe != nil {
			for _, ob := range oe.Bullets {
				if !used[ob.Hash] {
					for _, f := range ob.Facts {
						if f.Source == "inferred" && (f.Status == "approved" || f.Status == "rejected") { // rejections persist (PRD §6.1)
							keep = append(keep, f)
						}
					}
					if ob.Hash != InferredHash {
						st.Dropped++
					}
				}
			}
		}
		if len(fresh) > 0 {
			texts := make([]string, len(fresh))
			for j, i := range fresh {
				texts[j] = e.Bullets[i].Text
			}
			facts, calls, err := decompose(ctx, ol, system, e, texts)
			st.Calls += calls
			if err != nil {
				return nil, st, fmt.Errorf("%s: %w", e.ID, err)
			}
			for j, i := range fresh {
				next++
				b := &e.Bullets[i]
				for k, f := range facts[j] {
					f.ID = e.ID + "/" + strconv.Itoa(next) + string(rune('a'+k))
					b.Facts = append(b.Facts, f)
				}
				st.New++
			}
		}
		if len(keep) > 0 {
			e.Bullets = append(e.Bullets, Bullet{Hash: InferredHash, Render: new(bool), Facts: dedupe(keep)})
		}
		for _, b := range e.Bullets {
			st.Facts += len(b.Facts)
		}
		st.Bullets += len(e.Bullets)
		out.Entries = append(out.Entries, e)
	}
	st.Entries = len(out.Entries)
	out.Entities = entityMap(out.Entries)
	return out, st, nil
}

func dedupe(fs []Fact) []Fact {
	seen := map[string]bool{}
	return slices.DeleteFunc(fs, func(f Fact) bool {
		d := seen[f.ID]
		seen[f.ID] = true
		return d
	})
}

// factNum reads the bullet number out of "<entry>/<n><letter>".
func factNum(id string) int {
	_, rest, _ := strings.Cut(id, "/")
	n, _ := strconv.Atoi(strings.TrimRight(rest, "abcdefghijklmnopqrstuvwxyz"))
	return n
}

// entityMap groups every entity the facts name by case-insensitive spelling.
// The first spelling met is the canonical one. Postgres and PostgreSQL stay two
// entries: deciding they are one entity is a judgement the review makes
// (PRD §6.1), and a merge here that was wrong would be silent.
func entityMap(es []Entry) map[string][]string {
	canon := map[string]string{}
	forms := map[string][]string{}
	for _, e := range es {
		for _, b := range e.Bullets {
			for _, f := range b.Facts {
				for _, n := range f.Entities {
					k := strings.ToLower(n)
					c, ok := canon[k]
					if !ok {
						c = n
						canon[k] = n
					}
					if !slices.Contains(forms[c], n) {
						forms[c] = append(forms[c], n)
					}
				}
			}
		}
	}
	return forms
}

// chunk is how many bullets one decomposition call carries.
const chunk = 4

type rawFact struct {
	Text     string   `json:"text"`
	Entities []string `json:"entities"`
	Figures  []string `json:"figures"`
	MustKeep bool     `json:"must_keep"`
}

func schema() ollama.Schema {
	fact := ollama.Object(ollama.Obj(
		ollama.F("text", ollama.Obj(ollama.F("type", "string"), ollama.F("maxLength", 200))),
		ollama.F("entities", ollama.Arr(ollama.Str(), 8)),
		ollama.F("figures", ollama.Arr(ollama.Str(), 8)),
		ollama.F("must_keep", ollama.Obj(ollama.F("type", "boolean"))),
	))
	bullet := ollama.Object(ollama.Obj(
		ollama.F("n", ollama.Int(1, 40)),
		ollama.F("facts", ollama.Arr(fact, 8)),
	))
	return ollama.Object(ollama.Obj(ollama.F("bullets", ollama.Arr(bullet, 40))))
}

// decompose is one call for one entry. The model numbers the bullets it was
// given and answers by number, so a bullet is never matched to its facts by
// the model's copy of its text.
//
// TODO(M1+): a second pass proposing framings (PRD §6.1) from past reports'
// Block B phrases. Until then Framings stays empty and review adds none.
//
// The bullets go in chunks: one call for UBWIS's whole entry ran into the 3000
// token output cap (done_reason length, 125 s) and returned cut-off JSON, and
// the schema spends tokens on empty entity and figure arrays for every fact.
func decompose(ctx context.Context, ol *ollama.Client, system string, e Entry, texts []string) ([][]Fact, int, error) {
	var out [][]Fact
	calls := 0
	for i := 0; i < len(texts); i += chunk {
		part, err := decomposeChunk(ctx, ol, system, e, texts[i:min(i+chunk, len(texts))])
		calls++
		if err != nil {
			return nil, calls, err
		}
		out = append(out, part...)
	}
	return out, calls, nil
}

func decomposeChunk(ctx context.Context, ol *ollama.Client, system string, e Entry, texts []string) ([][]Fact, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Entry: %s\n", e.Title)
	if len(e.Qualifiers) > 0 {
		fmt.Fprintf(&b, "Qualifiers: %s\n", strings.Join(e.Qualifiers, ", "))
	}
	b.WriteString("\nBullets\n")
	for i, t := range texts {
		fmt.Fprintf(&b, "%d. %s\n", i+1, t)
	}
	var reply struct {
		Bullets []struct {
			N     int       `json:"n"`
			Facts []rawFact `json:"facts"`
		} `json:"bullets"`
	}
	if err := ol.Chat(ctx, ollama.Request{
		Model: ollama.ModelEval, System: system, User: b.String(), Schema: schema(),
		Options: ollama.Options{Temperature: 0, NumCtx: 8192, NumPredict: 3000},
		Timeout: 300 * time.Second, Label: "p3.ledger",
	}, &reply); err != nil {
		return nil, err
	}
	got := map[int][]rawFact{}
	for _, r := range reply.Bullets {
		got[r.N] = r.Facts
	}
	out := make([][]Fact, len(texts))
	for i, t := range texts {
		out[i] = verify(t, e.Title, e.Qualifiers, got[i+1])
	}
	return out, nil
}

// TidyAll applies Tidy to a ledger built before Tidy existed. A fact the user
// has already decided on (any Status) is left alone, and so is a whole-bullet
// fallback fact.
func TidyAll(l *Ledger) (changed int) {
	for ei := range l.Entries {
		e := &l.Entries[ei]
		for bi := range e.Bullets {
			b := &e.Bullets[bi]
			for fi := range b.Facts {
				f := &b.Facts[fi]
				if f.Status != "" || f.Source != "" || f.Text == b.Text {
					continue
				}
				before := *f
				before.Figures = slices.Clone(f.Figures)
				Tidy(f, e.Qualifiers)
				if f.MustKeep != before.MustKeep || !slices.Equal(f.Figures, before.Figures) {
					changed++
				}
			}
		}
	}
	return changed
}
