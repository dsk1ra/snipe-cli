package phase3

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
)

// The LLM rerank, ported from cv-select.mjs's judgeGrades and goldset.mjs's
// loadExemplars.
//
// snipe-eval grades every CV bullet 0-3 for the posting, few-shot from the gold
// set, and the grade is blended into the score as a flat JudgeBlend per point.
// Its prompt is prompts/p3-judge.md, byte for byte the Node's JUDGE_SYSTEM: the
// exemplars were graded against that wording and the +0.10 was measured with it.

// Shot is one gold-set exemplar: a past posting and what the human kept.
type Shot struct {
	Reqs []string `json:"reqs"`
	JD   string   `json:"jd"`
	// Want is the atom text the human kept. Grades, when the file was exported
	// graded, is per bullet and covers projects, which Want cannot.
	Want   map[string]bool `json:"want"`
	Grades map[string]int  `json:"grades,omitempty"`
}

type shotFile struct {
	Shots []struct {
		Reqs      []string       `json:"reqs"`
		JD        string         `json:"jd"`
		WantText  []string       `json:"wantText"`
		GradeText map[string]int `json:"gradeText"`
	} `json:"shots"`
}

var headMarkup = regexp.MustCompile(`^#+\s+|\*\*`)

// LoadShots reads the exemplars and drops every one that no longer matches cv.md.
//
// A bullet reworded since labelling makes that exemplar a lie about what the
// human picked. A missing exemplar is recoverable, a wrong one teaches the judge
// the opposite of the intended taste. Both an entry's bullets and a project's
// title count as known, because the exemplar file names either. A missing or
// unreadable file is no exemplars, not an error.
func LoadShots(path, cv string) []Shot {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f shotFile
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	known := map[string]bool{}
	for _, name := range []string{"Experience", "Projects"} {
		for _, s := range cvselect.ParseSections(cv) {
			if s.Name != name {
				continue
			}
			for _, e := range cvselect.ParseEntries(s.Lines).Entries {
				for _, b := range e.Bullets {
					known[b] = true
				}
				if name == "Projects" && len(e.Head) > 0 {
					known[strings.TrimSpace(headMarkup.ReplaceAllString(e.Head[0], ""))] = true
				}
			}
		}
	}
	var out []Shot
shots:
	for _, s := range f.Shots {
		if len(s.WantText) == 0 {
			continue
		}
		want := map[string]bool{}
		for _, t := range s.WantText {
			if !known[t] {
				continue shots
			}
			want[t] = true
		}
		out = append(out, Shot{Reqs: s.Reqs, JD: s.JD, Want: want, Grades: s.GradeText})
	}
	return out
}

// judgeKey identifies what the judge was told, for the p3.select cache. Empty
// means the judge is off, so a run before exemplars existed is never reused as
// one that had them, and a reworded prompt or a re-ticked sheet is a new key.
func judgeKey(system string, shots []Shot) string {
	if len(shots) == 0 {
		return ""
	}
	b, _ := json.Marshal(shots) // map keys marshal sorted, so this is stable
	sum := sha1.Sum(append([]byte(system+"\x00"), b...))
	return hex.EncodeToString(sum[:8])
}

// judgeSetup loads the prompt and the exemplars, or reports the judge off. Off
// is not an error: cosine plus the spike term is a complete score.
func (t *Tailor) judgeSetup() (string, []Shot) {
	shots := LoadShots(t.L.JudgeShots(), t.Cfg.CVRaw)
	if len(shots) == 0 {
		return "", nil
	}
	b, err := os.ReadFile(t.L.Prompt("p3-judge.md"))
	if err != nil {
		fmt.Printf("  judge off: %v\n", err)
		return "", nil
	}
	return strings.TrimRight(string(b), "\n"), shots
}

// BenchScores is scoreBullets split in two for gate judge, with the exemplars
// given rather than read from JudgeShots: base is the score with the judge
// off, grades the judge's 0-3 per bullet. The live blend is
// base + cvselect.JudgeBlend*grade.
func (t *Tailor) BenchScores(ctx context.Context, reqs []string, jd string, shots []Shot) (base, grades map[string]float64, err error) {
	if base, err = t.scoreBullets(ctx, reqs, jd, "", nil); err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(t.L.Prompt("p3-judge.md"))
	if err != nil {
		return nil, nil, err
	}
	grades, err = judgeGrades(ctx, t.OL, strings.TrimRight(string(b), "\n"), shots, cvBullets(t.Cfg.CVRaw), reqs, jd)
	return base, grades, err
}

type grade struct {
	ID    int     `json:"id"`
	Grade float64 `json:"grade"`
}

type gradeReply struct {
	Grades []grade `json:"grades"`
}

func judgeUser(reqs []string, jd, list string) string {
	var b strings.Builder
	b.WriteString("## Requirements\n\n")
	for i, r := range reqs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- " + r)
	}
	if jd != "" {
		if r := []rune(jd); len(r) > 2500 {
			jd = string(r[:2500])
		}
		b.WriteString("\n\n## Posting (excerpt)\n\n" + jd)
	}
	b.WriteString("\n\n## Candidate CV items\n\n" + list + "\n\nGrade every item 0-3 for this posting.")
	return b.String()
}

// judgeSchema keeps the Node's key order and adds no bounds: the grammar Ollama
// compiles from it is part of what was measured.
var judgeSchema = ollama.Object(ollama.Obj(ollama.F("grades", ollama.Arr(
	ollama.Object(ollama.Obj(
		ollama.F("id", ollama.Obj(ollama.F("type", "integer"))),
		ollama.F("grade", ollama.Obj(ollama.F("type", "integer"))),
	)), 0))))

// judgeGrades grades each bullet 0-3, keyed by bullet text. Any failure is an
// error and the caller falls back to cosine; zero exemplars is one, because
// 0-shot the judge grades almost everything 2 or 3 and scores 0.670 pair
// accuracy against plain cosine's 0.756. Two exemplars lift it to 0.739 alone
// and 0.851 blended, which is what makes it worth a 30B call.
func judgeGrades(ctx context.Context, ol *ollama.Client, system string, shots []Shot, items, reqs []string, jd string) (map[string]float64, error) {
	if len(shots) == 0 {
		return nil, fmt.Errorf("no exemplars")
	}
	var lb strings.Builder
	for i, it := range items {
		if i > 0 {
			lb.WriteByte('\n')
		}
		fmt.Fprintf(&lb, "%d. %s", i+1, it)
	}
	list := lb.String()

	msgs := []ollama.Message{{Role: "system", Content: system}}
	for _, s := range shots {
		// A graded exemplar states a grade per bullet. The binary fallback cannot:
		// its project ticks are titles while every item here is a bullet, so all
		// the project bullets would demonstrate as 0 however the human ticked
		// them — and demonstrations beat the system prompt.
		r := gradeReply{Grades: make([]grade, len(items))}
		for i, it := range items {
			g := 0
			if s.Grades != nil {
				g = s.Grades[it]
			} else if s.Want[it] {
				g = 3
			}
			r.Grades[i] = grade{ID: i + 1, Grade: float64(g)}
		}
		ans, _ := json.Marshal(r)
		msgs = append(msgs,
			ollama.Message{Role: "user", Content: judgeUser(s.Reqs, s.JD, list)},
			ollama.Message{Role: "assistant", Content: string(ans)})
	}
	msgs = append(msgs, ollama.Message{Role: "user", Content: judgeUser(reqs, jd, list)})

	var reply gradeReply
	err := ol.Chat(ctx, ollama.Request{
		Model: ollama.ModelEval, Messages: msgs, Schema: judgeSchema,
		Options: ollama.Options{Temperature: 0, NumCtx: 12288, NumPredict: 1536},
		Timeout: 180 * time.Second, Label: "p3.judge",
	}, &reply)
	if err != nil {
		return nil, err
	}
	// The per-item id looks redundant — the order is fixed by the prompt — and
	// dropping it for a bare positional array would save ~660 output tokens, some
	// 35 s of this call. Measured on goldset-2 it costs 0.052 pair accuracy
	// (0.930 -> 0.878), roughly half the judge's entire value: it is what keeps
	// the model aligned and deliberate per item.
	out := map[string]float64{}
	for _, e := range reply.Grades {
		if e.ID >= 1 && e.ID <= len(items) {
			out[items[e.ID-1]] = min(3, max(0, e.Grade))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("judge graded nothing")
	}
	return out, nil
}
