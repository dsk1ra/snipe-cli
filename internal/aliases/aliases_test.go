package aliases

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
)

// The text edit keeps the user's file as written and changes one thing.
func TestAddAlias(t *testing.T) {
	const src = `# my notes
aliases:
  REST APIs: [RESTful, REST API]  # keep this comment
  Go: [Golang]
# trailing note
other: 1
`
	for _, tc := range []struct {
		name, item, term string
		want             []string // lines that must be present afterwards
	}{
		{"into an existing list", "rest apis", "RESTful APIs",
			[]string{"  REST APIs: [RESTful, REST API, RESTful APIs]  # keep this comment"}},
		{"a new entry at the end of the block", "Apache Kafka", "Kafka",
			[]string{"  Go: [Golang]\n  Apache Kafka: [Kafka]\n# trailing note"}},
		{"a key that needs quoting", "C#", "CSharp", []string{`  "C#": [CSharp]`}},
		{"already there", "Go", "golang", []string{"  Go: [Golang]\n# trailing"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "skills.yml")
			os.WriteFile(p, []byte(src), 0o644)
			if err := AddAlias(p, tc.item, tc.term); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(p)
			got := string(b)
			for _, w := range append(tc.want, "# my notes", "other: 1") {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
		})
	}

	t.Run("no file yet", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "config", "skills.yml")
		if err := AddAlias(p, "Apache Kafka", "Kafka"); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(p); string(b) != "aliases:\n  Apache Kafka: [Kafka]\n" {
			t.Errorf("wrote %q", b)
		}
	})

	t.Run("through a symlink, leaving the link in place", func(t *testing.T) {
		d := t.TempDir()
		live := filepath.Join(d, "live.yml")
		os.WriteFile(live, []byte("aliases:\n  Go: [Golang]\n"), 0o644)
		link := filepath.Join(d, "dev.yml")
		os.Symlink(live, link)
		if err := AddAlias(link, "Apache Kafka", "Kafka"); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
			t.Error("the link was replaced by a file")
		}
		if b, _ := os.ReadFile(live); !strings.Contains(string(b), "Apache Kafka: [Kafka]") {
			t.Errorf("the linked file is %q", b)
		}
	})

	t.Run("a block list is refused, not rewritten", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "skills.yml")
		const block = "aliases:\n  Go:\n    - Golang\n"
		os.WriteFile(p, []byte(block), 0o644)
		if err := AddAlias(p, "Go", "Go lang"); err == nil {
			t.Error("edited a block-style list")
		}
		if b, _ := os.ReadFile(p); string(b) != block {
			t.Errorf("file changed to %q", b)
		}
	})
}

// Suggest end to end against a fake embedder: covered terms and one-posting
// terms never reach it, a distant term is dropped, and a rejection survives.
func TestSuggest(t *testing.T) {
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	for _, d := range []string{l.Evals(), l.Reports(), l.JDs(), l.StateDir()} {
		os.MkdirAll(d, 0o755)
	}
	for id, kw := range map[int]string{
		1: "Kafka, Java 17, agile, Spring, mentorship",
		2: "kafka, agile, mentorship",
	} {
		name := filepath.Base(l.Reports()) + "-" + string(rune('0'+id)) + ".md"
		os.WriteFile(filepath.Join(l.Reports(), name), []byte("# r\n\n## Keywords\n\n"+kw+"\n"), 0o644)
		// The id as real evals carry it: a string.
		e, _ := json.Marshal(map[string]any{"id": string(rune('0' + id)), "report_filename": name})
		os.WriteFile(filepath.Join(l.Evals(), string(rune('0'+id))+".json"), e, 0o644)
	}
	os.WriteFile(filepath.Join(l.JDs(), "1.txt"), []byte("We use Kafka."), 0o644)
	Save(l.SkillSuggestions(), []Suggestion{{Term: "mentorship", Rejected: true, Targets: []Target{{"Mentoring", 0.77}}}})

	vec := map[string][]float64{
		"Apache Kafka": {1, 0, 0}, "Java": {0, 0, 1}, "Mentoring": {0, 1, 0},
		"Kafka": {0.95, 0.1, 0}, "agile": {0.3, 0.3, 0.3},
	}
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		json.NewDecoder(r.Body).Decode(&req)
		var out struct {
			Embeddings [][]float64 `json:"embeddings"`
		}
		for _, in := range req.Input {
			in = strings.TrimPrefix(in, instruct)
			asked = append(asked, in)
			out.Embeddings = append(out.Embeddings, vec[in])
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	cfg := &config.Config{CVRaw: "## Skills\n\n**Backend:** Apache Kafka, Java\n**Practice:** Mentoring\n"}
	n, err := Suggest(context.Background(), l, cfg, ollama.New(srv.URL, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range asked {
		if a == "Java 17" || a == "Spring" || a == "mentorship" {
			t.Errorf("embedded %q, which should have been filtered first", a)
		}
	}
	ss, _ := Load(l.SkillSuggestions())
	if n != 1 || len(ss) != 2 {
		t.Fatalf("pending %d, rows %+v; want Kafka pending and mentorship still rejected", n, ss)
	}
	k := ss[0]
	if k.Term != "Kafka" || k.Targets[0].Item != "Apache Kafka" || len(k.IDs) != 2 || k.Rejected {
		t.Errorf("first row %+v, want Kafka → Apache Kafka from both offers", k)
	}
	if !ss[1].Rejected || ss[1].Term != "mentorship" {
		t.Errorf("second row %+v, want the rejection kept", ss[1])
	}

	// Accepting writes the alias and takes the row off the queue.
	if err := Accept(l, "Apache Kafka", "Kafka"); err != nil {
		t.Fatal(err)
	}
	if ss, _ := Load(l.SkillSuggestions()); len(ss) != 1 || ss[0].Term != "mentorship" {
		t.Errorf("queue after accept: %+v", ss)
	}
	if b, _ := os.ReadFile(l.SkillsYML()); !strings.Contains(string(b), "Apache Kafka: [Kafka]") {
		t.Errorf("skills.yml: %q", b)
	}
}
