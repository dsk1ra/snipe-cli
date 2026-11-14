package verify

import (
	"context"
	"hash/fnv"
	"regexp"
	"strings"
	"unicode"

	"github.com/dsk1ra/snipe-cli/internal/guards"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
)

// The E2 seeded set (docs/PRD-TAILORING-REWORK.md §8.2). Corruptions are made
// false by construction, so they need no human label; only the trivially
// faithful variant is ticked by a person, because a rewrite that changes only
// whitespace or a full stop can still land on a bullet where that matters.

const (
	KindTool      = "added-tool"
	KindQualifier = "dropped-qualifier"
	KindScope     = "inflated-scope"
	KindFigure    = "moved-figure"
	KindFaithful  = "faithful"
)

type Case struct {
	Source        string
	Draft         string
	Kind          string
	WantSupported bool
	NeedsTick     bool // a human must confirm the label before it counts
}

// toolPool is deliberately small and fixed: a corruption only has to be a tool
// the entry does not mention, and a fixed pool makes the set reproducible.
var toolPool = []string{"Kubernetes", "Terraform", "Kafka", "Snowflake", "Airflow", "Redis", "Spark", "GraphQL"}

var qualifiers = []string{"simulated", "supervised", "basic", "prototype", "academic", "mock"}

// scope pairs a modest verb with the stronger one it is inflated to.
var scope = []struct{ weak, strong string }{
	{"built", "led"}, {"developed", "architected"}, {"implemented", "architected"},
	{"designed", "led"}, {"created", "architected"}, {"wrote", "led"},
	{"contributed to", "led"}, {"supported", "led"}, {"assisted", "led"},
}

func pick(seed string, n int) int {
	h := fnv.New32a()
	h.Write([]byte(seed))
	return int(h.Sum32() % uint32(n))
}

func wordRe(w string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(w) + `\b`)
}

func matchCase(from, to string) string {
	if from != "" && unicode.IsUpper([]rune(from)[0]) {
		return strings.ToUpper(to[:1]) + to[1:]
	}
	return to
}

// Cases makes the seeded set for one bullet: up to one case per corruption kind
// that applies to it, plus one faithful variant. A kind that cannot be built
// (no qualifier to drop, no other entry's figure to borrow) is skipped, so the
// size of the whole set depends on how many bullets the ledger has.
func Cases(led *ledger.Ledger, e ledger.Entry, b ledger.Bullet) []Case {
	src := strings.TrimSpace(b.Text)
	var out []Case
	add := func(kind, draft string) {
		if draft = strings.TrimSpace(draft); draft != "" && draft != src {
			out = append(out, Case{Source: src, Draft: draft, Kind: kind})
		}
	}

	var entryText strings.Builder
	for _, bb := range e.Bullets {
		entryText.WriteString(bb.Text + "\n")
	}
	et := strings.ToLower(entryText.String())
	n := len(toolPool)
	for i := 0; i < n; i++ {
		t := toolPool[(pick(src, n)+i)%n]
		if !strings.Contains(et, strings.ToLower(t)) {
			add(KindTool, strings.TrimRight(src, ". ")+" using "+t+".")
			break
		}
	}

	for _, q := range qualifiers {
		if re := wordRe(q); re.MatchString(src) {
			d := strings.Join(strings.Fields(re.ReplaceAllString(src, "")), " ")
			if d != "" {
				r := []rune(d)
				add(KindQualifier, string(unicode.ToUpper(r[0]))+string(r[1:]))
			}
			break
		}
	}

	for i := range scope {
		s := scope[(pick(src, len(scope))+i)%len(scope)]
		if re := wordRe(s.weak); re.MatchString(src) {
			add(KindScope, re.ReplaceAllStringFunc(src, func(m string) string { return matchCase(m, s.strong) }))
			break
		}
	}

	if f := foreignFigure(led, e, src); f != "" {
		done := false
		for _, loc := range figureTokens.FindAllStringIndex(src, -1) {
			if len(guards.NumbersIn(src[loc[0]:loc[1]])) > 0 {
				add(KindFigure, src[:loc[0]]+f+src[loc[1]:])
				done = true
				break
			}
		}
		if !done {
			add(KindFigure, strings.TrimRight(src, ". ")+", yielding "+f+".")
		}
	}

	faithful := strings.Replace(src, " ", "  ", 1)
	if strings.HasSuffix(faithful, ".") {
		faithful = strings.TrimSuffix(faithful, ".")
	} else {
		faithful += "."
	}
	if faithful != src {
		out = append(out, Case{Source: src, Draft: faithful, Kind: KindFaithful, WantSupported: true, NeedsTick: true})
	}
	return out
}

// figureTokens finds a figure with its unit ("10k", "65%"), so a swap replaces
// the whole thing rather than the digits inside it.
var figureTokens = regexp.MustCompile(`\d[\d,.]*[kKmMbB+%]*`)

func figuresOf(s string) []string {
	var out []string
	for _, m := range figureTokens.FindAllString(s, -1) {
		m = strings.TrimRight(m, ".,")
		if len(guards.NumbersIn(m)) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// foreignFigure is a figure that belongs to another entry and appears nowhere
// in this one, which is what a rewriter that crossed bullets would produce.
func foreignFigure(led *ledger.Ledger, e ledger.Entry, src string) string {
	var mine strings.Builder
	for _, b := range e.Bullets {
		mine.WriteString(b.Text + "\n")
	}
	haveTok := map[string]bool{}
	for _, f := range figuresOf(mine.String()) {
		haveTok[f] = true
	}
	var pool []string
	for _, o := range led.Entries {
		if o.ID == e.ID {
			continue
		}
		for _, b := range o.Bullets {
			for _, f := range figuresOf(b.Text) {
				if !haveTok[f] {
					pool = append(pool, f)
				}
			}
		}
	}
	if len(pool) == 0 {
		return ""
	}
	return pool[pick(src, len(pool))]
}

type Outcome struct {
	Case
	Flagged      bool // the verifier called the draft unsupported
	Attribution  bool // draft supported by the source
	Preservation bool // source supported by the draft
}

type Report struct {
	Outcomes []Outcome
	// Recall is the share of corruptions flagged; E2's gate is >= 0.95.
	Recall float64
	// Precision is the share of flagged drafts that really were corruptions;
	// the gate is >= 0.80. Both treat "flagged" as the positive class, so a
	// verifier that rejects everything has recall 1 and is caught by precision.
	Precision  float64
	ByKind     map[string]float64 // recall per corruption kind
	FalseAlarm int                // faithful drafts flagged
}

// Calibrate runs the model checks on every case, in both directions, without
// the code checks: E2 asks what the verifier catches that the code cannot. The
// source stands for the whole fact set, so preservation tests that the draft
// still supports each claim the source made. Pass the same Options.Split that
// production will use.
func Calibrate(ctx context.Context, c Checker, cases []Case, opts ...Options) (Report, error) {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	rep := Report{ByKind: map[string]float64{}}
	seen, caught := map[string]int{}, map[string]int{}
	corrupt, flagged, flaggedCorrupt := 0, 0, 0
	for _, cs := range cases {
		att, err := supports(ctx, c, cs.Source, o.split(cs.Draft))
		if err != nil {
			return rep, err
		}
		pres, err := supports(ctx, c, cs.Draft, o.split(cs.Source))
		if err != nil {
			return rep, err
		}
		out := Outcome{Case: cs, Attribution: att, Preservation: pres, Flagged: !att || !pres}
		rep.Outcomes = append(rep.Outcomes, out)

		if out.Flagged {
			flagged++
		}
		if cs.WantSupported {
			if out.Flagged {
				rep.FalseAlarm++
			}
			continue
		}
		corrupt++
		seen[cs.Kind]++
		if out.Flagged {
			flaggedCorrupt++
			caught[cs.Kind]++
		}
	}
	if corrupt > 0 {
		rep.Recall = float64(flaggedCorrupt) / float64(corrupt)
	}
	if flagged > 0 {
		rep.Precision = float64(flaggedCorrupt) / float64(flagged)
	}
	for k, n := range seen {
		rep.ByKind[k] = float64(caught[k]) / float64(n)
	}
	return rep, nil
}
