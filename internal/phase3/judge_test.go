package phase3

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
)

const judgeCV = `# Sam Lee

## Experience

### Backend Engineer
**Acme Ltd** — London, UK | Jan 2022 – Present
- Built REST services handling 40k req/s on Postgres
- Led a team of four through a payments migration

## Projects

### Tiny Tool
- Wrote a small tool in Rust
`

// fakeOllama embeds by text length and answers /api/chat with reply, recording
// every chat body.
type fakeOllama struct {
	mu       sync.Mutex
	chats    []map[string]any
	embedded int // texts sent to /api/embed
	reply    func() (int, string)
}

func (f *fakeOllama) serve(t *testing.T) *ollama.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/api/embed":
			var vecs [][]float64
			f.mu.Lock()
			f.embedded += len(body["input"].([]any))
			f.mu.Unlock()
			for _, s := range body["input"].([]any) {
				n := float64(len(s.(string)))
				vecs = append(vecs, []float64{1, math.Mod(n, 7), math.Mod(n, 5)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
		case "/api/chat":
			f.mu.Lock()
			f.chats = append(f.chats, body)
			f.mu.Unlock()
			code, content := f.reply()
			if code != 200 {
				http.Error(w, "boom", code)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": content}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return ollama.New(srv.URL, nil)
}

func judgeTailor(t *testing.T, ol *ollama.Client, shots string) *Tailor {
	t.Helper()
	repo, _ := filepath.Abs(filepath.Join("..", ".."))
	l := paths.Layout{Home: t.TempDir(), Code: repo}
	if shots != "" {
		os.MkdirAll(l.CacheDir(), 0o755)
		os.WriteFile(l.JudgeShots(), []byte(shots), 0o644)
	}
	return &Tailor{L: l, Cfg: &config.Config{CVRaw: judgeCV}, OL: ol}
}

const (
	b1 = "Built REST services handling 40k req/s on Postgres"
	b2 = "Led a team of four through a payments migration"
	b3 = "Wrote a small tool in Rust"
)

func shotsJSON(want ...string) string {
	b, _ := json.Marshal(map[string]any{"shots": []any{map[string]any{
		"reqs": []string{"Go", "Postgres"}, "jd": "A backend role.", "wantText": want,
	}}})
	return string(b)
}

func TestJudgeBlendsGrades(t *testing.T) {
	f := &fakeOllama{reply: func() (int, string) {
		return 200, `{"grades":[{"id":1,"grade":3},{"id":2,"grade":0},{"id":3,"grade":9}]}`
	}}
	on := judgeTailor(t, f.serve(t), shotsJSON(b1))
	off := judgeTailor(t, f.serve(t), "")
	got, err := on.ScoreBullets(context.Background(), []string{"Postgres"})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := off.ScoreBullets(context.Background(), []string{"Postgres"})
	// 9 clamps to 3.
	for b, g := range map[string]float64{b1: 3, b2: 0, b3: 3} {
		if d := got[b] - base[b]; math.Abs(d-cvselect.JudgeBlend*g) > 1e-9 {
			t.Errorf("%q: blended %+.4f, want %+.4f", b, d, cvselect.JudgeBlend*g)
		}
	}

	// The request is the Node's: eval model, temp 0, few-shot, shot answer is
	// the human's picks as 3 and the rest 0.
	if len(f.chats) != 1 {
		t.Fatalf("%d chat calls, want 1", len(f.chats))
	}
	c := f.chats[0]
	if c["model"] != ollama.ModelEval {
		t.Errorf("model %v", c["model"])
	}
	o := c["options"].(map[string]any)
	if o["temperature"] != 0.0 || o["num_ctx"] != 12288.0 || o["num_predict"] != 1536.0 {
		t.Errorf("options %v", o)
	}
	msgs := c["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("%d messages, want system, shot user, shot assistant, final user", len(msgs))
	}
	role := func(i int) string { return msgs[i].(map[string]any)["role"].(string) }
	text := func(i int) string { return msgs[i].(map[string]any)["content"].(string) }
	if role(0) != "system" || role(1) != "user" || role(2) != "assistant" || role(3) != "user" {
		t.Errorf("roles %v", msgs)
	}
	if !strings.HasPrefix(text(0), "You are a recruiter deciding") || !strings.HasSuffix(text(0), "Return one entry per item id.") {
		t.Errorf("system prompt not p3-judge.md: %q", text(0))
	}
	if want := `{"grades":[{"id":1,"grade":3},{"id":2,"grade":0},{"id":3,"grade":0}]}`; text(2) != want {
		t.Errorf("exemplar answer %s", text(2))
	}
	if !strings.Contains(text(3), "## Requirements\n\n- Postgres\n\n## Candidate CV items\n\n1. "+b1+"\n2. "+b2+"\n3. "+b3+"\n\nGrade every item 0-3 for this posting.") {
		t.Errorf("final user %q", text(3))
	}
	if !strings.Contains(text(1), "## Posting (excerpt)\n\nA backend role.") {
		t.Errorf("shot user lost its posting: %q", text(1))
	}
}

func TestJudgeNeedsExemplars(t *testing.T) {
	f := &fakeOllama{reply: func() (int, string) { return 200, `{"grades":[{"id":1,"grade":3}]}` }}
	for name, shots := range map[string]string{
		"no file":        "",
		"empty":          `{"shots":[]}`,
		"stale exemplar": shotsJSON(b1, "a bullet reworded since labelling"),
	} {
		tl := judgeTailor(t, f.serve(t), shots)
		if _, s := tl.judgeSetup(); len(s) != 0 {
			t.Errorf("%s: %d shots survived", name, len(s))
		}
		if _, err := tl.ScoreBullets(context.Background(), []string{"Postgres"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.chats) != 0 {
		t.Errorf("0-shot judge called the model %d times", len(f.chats))
	}
}

func TestJudgeDropsOnlyStaleExemplars(t *testing.T) {
	two, _ := json.Marshal(map[string]any{"shots": []any{
		map[string]any{"reqs": []string{"r"}, "jd": "j", "wantText": []string{b1}},
		map[string]any{"reqs": []string{"r"}, "jd": "j", "wantText": []string{b1, "gone"}},
		// A project is named by its title in the sheet.
		map[string]any{"reqs": []string{"r"}, "jd": "j", "wantText": []string{"Tiny Tool"}},
	}})
	if got := LoadShots(writeShots(t, string(two)), judgeCV); len(got) != 2 {
		t.Errorf("%d shots kept, want 2", len(got))
	}
}

func writeShots(t *testing.T, s string) string {
	p := filepath.Join(t.TempDir(), "judge-shots.json")
	os.WriteFile(p, []byte(s), 0o644)
	return p
}

func TestJudgeFailureFallsBackToCosine(t *testing.T) {
	off := judgeTailor(t, (&fakeOllama{}).serve(t), "")
	base, _ := off.ScoreBullets(context.Background(), []string{"Postgres"})
	for name, reply := range map[string]func() (int, string){
		"malformed":   func() (int, string) { return 200, `not json at all` },
		"no grades":   func() (int, string) { return 200, `{"grades":[]}` },
		"wrong ids":   func() (int, string) { return 200, `{"grades":[{"id":99,"grade":3}]}` },
		"server down": func() (int, string) { return 500, "" },
	} {
		tl := judgeTailor(t, (&fakeOllama{reply: reply}).serve(t), shotsJSON(b1))
		got, err := tl.ScoreBullets(context.Background(), []string{"Postgres"})
		if err != nil {
			t.Fatalf("%s: judge failure leaked: %v", name, err)
		}
		for b, s := range base {
			if got[b] != s {
				t.Errorf("%s: %q scored %v, want cosine %v", name, b, got[b], s)
			}
		}
	}
}

func TestJudgeKeyTracksShotsAndPrompt(t *testing.T) {
	load := func(want ...string) []Shot { return LoadShots(writeShots(t, shotsJSON(want...)), judgeCV) }
	a, b := load(b1), load(b1, b2)
	if judgeKey("p", nil) != "" {
		t.Error("judge off must key as empty")
	}
	if judgeKey("p", a) == "" || judgeKey("p", a) != judgeKey("p", load(b1)) {
		t.Error("same shots must key the same, and not empty")
	}
	if judgeKey("p", a) == judgeKey("p", b) {
		t.Error("different shots, same key")
	}
	if judgeKey("p", a) == judgeKey("q", a) {
		t.Error("different prompt, same key")
	}
	// A graded exemplar is different information from a binary one.
	g, _ := json.Marshal(map[string]any{"shots": []any{map[string]any{
		"reqs": []string{"Go", "Postgres"}, "jd": "A backend role.", "wantText": []string{b1},
		"gradeText": map[string]int{b1: 2}}}})
	if judgeKey("p", a) == judgeKey("p", LoadShots(writeShots(t, string(g)), judgeCV)) {
		t.Error("graded and binary exemplar, same key")
	}
}
