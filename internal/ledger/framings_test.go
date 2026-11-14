package ledger

import (
	"context"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// lexEmbed is a bag-of-words embedder: texts sharing words are close. It lets
// the tests rank candidates without Ollama and without a vector file.
func lexEmbed(calls *int) EmbedFunc {
	return func(_ context.Context, texts []string) ([][]float64, error) {
		if calls != nil {
			*calls += len(texts)
		}
		out := make([][]float64, len(texts))
		for i, t := range texts {
			v := make([]float64, 128)
			for _, w := range regexp.MustCompile(`[a-z0-9]+`).FindAllString(strings.ToLower(t), -1) {
				h := fnv.New32a()
				h.Write([]byte(strings.TrimSuffix(w, "s")))
				v[h.Sum32()%128]++
			}
			out[i] = v
		}
		return out, nil
	}
}

var numbered = regexp.MustCompile(`^(\d+)\. (.*)$`)

type shown struct{ facts, phrases []string }

// parseFramingPrompt reads back what the model was shown, per bullet.
func parseFramingPrompt(user string) []shown {
	var out []shown
	mode := ""
	for _, l := range strings.Split(user, "\n") {
		switch {
		case strings.HasPrefix(l, "Bullet "):
			out = append(out, shown{})
		case l == "Facts":
			mode = "f"
		case l == "Phrases":
			mode = "p"
		default:
			if m := numbered.FindStringSubmatch(l); m != nil && len(out) > 0 {
				s := &out[len(out)-1]
				if mode == "f" {
					s.facts = append(s.facts, m[2])
				} else {
					s.phrases = append(s.phrases, m[2])
				}
			}
		}
	}
	return out
}

// pick answers every bullet by number: the phrase want[phrase] = fact numbers
// when it was shown, nothing else.
func pick(want map[string][]int) func(string) any {
	return func(user string) any {
		var bs []m
		for i, s := range parseFramingPrompt(user) {
			var ps []m
			for j, p := range s.phrases {
				from, ok := want[p]
				ps = append(ps, m{"c": j + 1, "supported": ok, "from": append([]int{}, from...)})
			}
			bs = append(bs, m{"n": i + 1, "phrases": ps})
		}
		return m{"bullets": bs}
	}
}

func framed() *Ledger {
	return &Ledger{
		Entities: map[string][]string{"Kafka": {"Kafka"}, "Jenkins": {"Jenkins"}},
		Entries: []Entry{{ID: "de-store", Title: "Store Platform", Bullets: []Bullet{{
			Hash: "h1", Text: "Built 10 services talking only through a Kafka log",
			Facts: []Fact{
				{ID: "de-store/1a", Text: "Built 10 Java services"},
				{ID: "de-store/1b", Text: "The services talk only through a Kafka log"},
				{ID: "de-store/1c", Text: "Each service owns its database"},
			},
		}}}},
	}
}

var corpus = []Phrase{
	{"event-driven microservices", 9}, {"database per service", 4},
	{"services talking through a log", 3}, {"kafka cluster tuning", 2}, {"leading a team", 2},
}

func TestCollectPhrases(t *testing.T) {
	rep := func(rows ...string) string {
		return "# Eval\n\n| JD Requirement | Candidate evidence | Strength |\n|---|---|---|\n" + strings.Join(rows, "\n") + "\n\n## Keywords\n\nKafka, event-driven microservices, Docker, containerization\n"
	}
	r1 := rep(
		"| **[must]** Experience with event-driven microservices (Kafka, SQS) | x | Strong |",
		"| Strong experience with Kafka and Jenkins | x | Strong |",
		"| Familiarity with cloud infrastructure and containerization | x | Gap |",
	)
	r2 := rep("| Experience building event-driven microservices | x | Strong |")
	ents := map[string][]string{"Kafka": {"Kafka"}, "Jenkins": {"Jenkins"}, "Docker": {"Docker"}}
	got := CollectPhrases([]string{r1, r2}, ents, 2)
	byText := map[string]int{}
	for _, p := range got {
		byText[p.Text] = p.Count
	}
	if byText["event-driven microservices"] != 2 {
		t.Errorf("count across postings: %v", byText)
	}
	if byText["containerization"] != 2 {
		t.Errorf("known single-word capability dropped: %v", byText)
	}
	for _, bad := range []string{"kafka", "kafka and jenkins", "docker", "cloud infrastructure"} {
		if _, ok := byText[bad]; ok {
			t.Errorf("%q must not be in the corpus (entity name, or seen once): %v", bad, byText)
		}
	}
	if len(got) != 2 {
		t.Errorf("corpus: %v", got)
	}
}

func TestFrameMapsNumbersBackToCorpusPhrases(t *testing.T) {
	ol, asked := fake(t, pick(map[string][]int{
		"event-driven microservices": {3, 2, 2},
		"database per service":       {3},
	}))
	l := framed()
	st, err := Frame(context.Background(), ol, "sys", l, corpus, lexEmbed(nil), FramingOpts{K: 8}, "2026-10-21")
	if err != nil {
		t.Fatal(err)
	}
	b := l.Entries[0].Bullets[0]
	if len(b.Framings) != 2 || b.FramedAt != "2026-10-21" || st.Calls != 1 || st.Proposed["de-store"] != 2 {
		t.Fatalf("framings %+v stats %+v", b, st)
	}
	for _, f := range b.Framings {
		if f.Approved || f.Rejected {
			t.Errorf("proposed framing must be pending: %+v", f)
		}
	}
	// The ids are the ledger's own, in fact order and without repeats.
	if f := b.Framings[slices.IndexFunc(b.Framings, func(f Framing) bool { return f.Phrase == "event-driven microservices" })]; !slices.Equal(f.From, []string{"de-store/1b", "de-store/1c"}) {
		t.Errorf("from: %v", f.From)
	}
	for _, p := range parseFramingPrompt((*asked)[0])[0].phrases {
		if !slices.ContainsFunc(corpus, func(c Phrase) bool { return c.Text == p }) {
			t.Errorf("prompt offered %q, which is not a corpus phrase", p)
		}
	}
}

func TestFrameDropsOutOfRangeNumbers(t *testing.T) {
	ol, _ := fake(t, func(user string) any {
		return m{"bullets": []m{
			{"n": 1, "phrases": []m{
				{"c": 99, "supported": true, "from": []int{1}},   // no such candidate
				{"c": 1, "supported": true, "from": []int{9}},    // no such fact
				{"c": 2, "supported": true, "from": []int{}},     // no evidence named
				{"c": 3, "supported": true, "from": []int{0}},    // numbering starts at 1
				{"c": 4, "supported": false, "from": []int{1}},   // model said no
				{"c": 5, "supported": true, "from": []int{1}},    // the one good answer
				{"c": 5, "supported": true, "from": []int{2}},    // repeated candidate
				{"c": -1, "supported": true, "from": []int{1}},   // nonsense
				{"c": 1, "supported": true, "from": []int{1, 7}}, // one bad fact spoils the answer
			}},
			{"n": 7, "phrases": []m{{"c": 1, "supported": true, "from": []int{1}}}}, // no such bullet
		}}
	})
	l := framed()
	st, err := Frame(context.Background(), ol, "sys", l, corpus, lexEmbed(nil), FramingOpts{K: 8}, "d")
	if err != nil {
		t.Fatal(err)
	}
	fs := l.Entries[0].Bullets[0].Framings
	if len(fs) != 1 || len(fs[0].From) != 1 {
		t.Fatalf("only the one valid answer survives: %+v", fs)
	}
	if st.Dropped != 8 {
		t.Errorf("dropped = %d", st.Dropped)
	}
}

func TestFrameLeavesDecisionsAlone(t *testing.T) {
	l := framed()
	b := &l.Entries[0].Bullets[0]
	b.Framings = []Framing{
		{Phrase: "event-driven microservices", From: []string{"de-store/1b"}, Approved: true},
		{Phrase: "leading a team", From: []string{"de-store/1a"}, Rejected: true},
	}
	ol, asked := fake(t, pick(map[string][]int{"database per service": {3}, "leading a team": {1}, "event-driven microservices": {2}}))
	if _, err := Frame(context.Background(), ol, "sys", l, corpus, lexEmbed(nil), FramingOpts{K: 8}, "d"); err != nil {
		t.Fatal(err)
	}
	for _, p := range parseFramingPrompt((*asked)[0])[0].phrases {
		if p == "leading a team" || p == "event-driven microservices" {
			t.Errorf("decided phrase %q offered again", p)
		}
	}
	fs := l.Entries[0].Bullets[0].Framings
	if len(fs) != 3 || !fs[0].Approved || !fs[1].Rejected || fs[2].Phrase != "database per service" || fs[2].Approved {
		t.Fatalf("framings: %+v", fs)
	}
}

func TestFrameSkipsFramedBulletsAndRefreshKeepsDecided(t *testing.T) {
	l := framed()
	ol, asked := fake(t, pick(map[string][]int{"database per service": {3}}))
	run := func(refresh bool) {
		t.Helper()
		if _, err := Frame(context.Background(), ol, "sys", l, corpus, lexEmbed(nil), FramingOpts{Refresh: refresh}, "d"); err != nil {
			t.Fatal(err)
		}
	}
	run(false)
	run(false)
	if len(*asked) != 1 {
		t.Fatalf("second run must skip the framed bullet, calls = %d", len(*asked))
	}
	// Refresh replaces an undecided proposal, and re-proposes it here.
	run(true)
	if len(*asked) != 2 || len(l.Entries[0].Bullets[0].Framings) != 1 {
		t.Fatalf("refresh: calls %d, framings %+v", len(*asked), l.Entries[0].Bullets[0].Framings)
	}
	// Once something is decided, refresh leaves the bullet alone.
	l.Entries[0].Bullets[0].Framings[0].Approved = true
	run(true)
	if len(*asked) != 2 {
		t.Errorf("refresh redid a bullet with a decision on it")
	}
}

func TestFrameChunksAtFour(t *testing.T) {
	l := framed()
	for i := 2; i <= 9; i++ {
		b := l.Entries[0].Bullets[0]
		b.Hash = fmt.Sprintf("h%d", i)
		l.Entries[0].Bullets = append(l.Entries[0].Bullets, b)
	}
	// An inferred-facts holder is not a bullet and is never sent.
	l.Entries[0].Bullets = append(l.Entries[0].Bullets, Bullet{Hash: InferredHash, Facts: []Fact{{ID: "de-store/9a", Text: "x"}}})
	if b, c := FrameDiff(l, false); b != 9 || c != 3 {
		t.Errorf("FrameDiff = %d bullets, %d calls", b, c)
	}
	ol, asked := fake(t, pick(nil))
	st, err := Frame(context.Background(), ol, "sys", l, corpus, lexEmbed(nil), FramingOpts{}, "d")
	if err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, u := range *asked {
		sizes = append(sizes, len(parseFramingPrompt(u)))
	}
	if !slices.Equal(sizes, []int{4, 4, 1}) || st.Calls != 3 {
		t.Errorf("chunks %v, calls %d", sizes, st.Calls)
	}
}

// A phrase naming an entity the bullet never mentions is not offered, and a
// bullet with nothing above the floor costs no call but is marked done.
func TestFrameGuardAndFloor(t *testing.T) {
	l := framed()
	ps := []Phrase{{"jenkins pipeline automation", 5}, {"event-driven microservices", 5}}
	ol, asked := fake(t, pick(nil))
	if _, err := Frame(context.Background(), ol, "sys", l, ps, lexEmbed(nil), FramingOpts{}, "d"); err != nil {
		t.Fatal(err)
	}
	if got := parseFramingPrompt((*asked)[0])[0].phrases; !slices.Equal(got, []string{"event-driven microservices"}) {
		t.Errorf("offered %v", got)
	}

	l = framed()
	*asked = nil
	st, err := Frame(context.Background(), ol, "sys", l, corpus, lexEmbed(nil), FramingOpts{Floor: 0.99}, "d")
	if err != nil || len(*asked) != 0 || st.NoCandidates != 1 || l.Entries[0].Bullets[0].FramedAt != "d" {
		t.Errorf("floor: err %v calls %d stats %+v framed %q", err, len(*asked), st, l.Entries[0].Bullets[0].FramedAt)
	}
}

func TestCachedEmbedOnlyEmbedsMisses(t *testing.T) {
	var n int
	p := filepath.Join(t.TempDir(), "cache", "framings-embed.json")
	e := CachedEmbed(p, "fp1", lexEmbed(&n))
	a, _ := e(context.Background(), []string{"one two", "three"})
	b, _ := e(context.Background(), []string{"three", "one two", "four"})
	if n != 3 || !slices.Equal(a[0], b[1]) {
		t.Errorf("embedded %d texts", n)
	}
	CachedEmbed(p, "fp2", lexEmbed(&n))(context.Background(), []string{"three"})
	if n != 4 {
		t.Errorf("a new fingerprint must discard the cache, embedded %d", n)
	}
}
