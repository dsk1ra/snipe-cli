package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/ollama"
)

func TestGateFitx(t *testing.T) {
	home := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("prompts/p1-extract.md", "extract")
	write("prompts/p1-cvstack.md", "cvstack")
	write("cv.md", "**Languages**\n- English: Advanced\n")
	write("config/profile.yml", "location:\n  city: \"Ashcombe\"\n  search_locations:\n    - \"London — fully remote only\"\n")
	write("batch/jds/1.txt", "Backend Engineer\nLocation: London. Hybrid, 3 days a week in the office.")
	write("batch/jds/2.txt", "We have multiple openings across the company.")
	// Berlin is not in ukCities, so only the facts path caps it.
	write("batch/jds/3.txt", "Backend Engineer\nLocation: Berlin. Hybrid, 3 days a week in the office.")

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct{ Messages []ollama.Message }
		_ = json.NewDecoder(r.Body).Decode(&req)
		place := "London"
		if strings.Contains(req.Messages[len(req.Messages)-1].Content, "Berlin") {
			place = "Berlin"
		}
		reply := `{"attendance": {"quote": "Hybrid, 3 days a week in the office", "mode": "hybrid", "days_per_week": 3, "days_per_month": 0, "place": "` + place + `"},
		 "languages": [], "years": {"quote": "", "min_years": 0}, "stack": []}`
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": reply}})
	}))
	defer srv.Close()
	old := fitxClient
	fitxClient = func() *ollama.Client { return ollama.New(srv.URL, nil) }
	defer func() { fitxClient = old }()

	if rc := gateFitx([]string{home}); rc != 0 {
		t.Fatalf("exit %d", rc)
	}
	if calls != 2 {
		t.Errorf("%d model calls, want 2: the multi-posting page is skipped, and with no ledger there is no CV call", calls)
	}
	b, err := os.ReadFile(filepath.Join(home, "batch", "cache", "fitx-disagreements.md"))
	if err != nil {
		t.Fatal(err)
	}
	rep := string(b)
	for _, want := range []string{"| location | 1/2 |", "### 3 (location)", "regex: no cap", "facts: cap 2/2 berlin", "place=\"Berlin\"", "1 multi-posting skipped", "| attendance | 2 | 2 | 100% |"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
	if strings.Contains(rep, "### 1 (location)") {
		t.Error("an agreement was listed as a disagreement")
	}

	// A second run reads the cache and calls nothing.
	if rc := gateFitx([]string{home, "--only", "location"}); rc != 0 || calls != 2 {
		t.Errorf("second run: exit %d, %d calls", rc, calls)
	}
	if gateFitx([]string{home, "--only", "salary"}) != 2 {
		t.Error("an unknown --only should be a usage error")
	}
}
