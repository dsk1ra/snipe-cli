package ledger

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/ollama"
)

const cvMD = `**Experience**

**Software Engineer**

**UBWIS** - Edinburgh, United Kingdom | Oct 2024 – Sep 2025
- Grew a members' club from 80 to 200 paying members using Stripe billing
- Cut newsletter sends from 8 minutes to 28 seconds with Redis

**Projects**
**Re:Link - Peer-to-Peer Remote Access**

**Honours Dissertation** | Rust, QUIC | 2025

github.com/dsk1ra/re-link
- Built a remote-desktop tool in Rust with AES-256-GCM encryption
  that wraps onto a second source line
`

const digestMD = "# Digest\n\n- Holds the right to work in the UK without sponsorship\n- short\n"

// fake answers every request with reply(user prompt) and records the prompts.
func fake(t *testing.T, reply func(user string) any) (*ollama.Client, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string }
		}
		json.NewDecoder(r.Body).Decode(&req)
		user := req.Messages[len(req.Messages)-1].Content
		asked = append(asked, user)
		body, _ := json.Marshal(reply(user))
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": string(body)}})
	}))
	t.Cleanup(srv.Close)
	return ollama.New(srv.URL, nil), &asked
}

type m = map[string]any

// good answers every numbered bullet with its own text as one honest fact.
func good(user string) any {
	var bs []m
	for _, l := range strings.Split(user, "\n") {
		if n := strings.Index(l, ". "); n > 0 && n < 3 {
			bs = append(bs, m{"n": int(l[0] - '0'), "facts": []m{{"text": l[n+2:], "entities": []string{}, "figures": []string{}, "must_keep": false}}})
		}
	}
	return m{"bullets": bs}
}

func TestParse(t *testing.T) {
	es := Parse(cvMD, digestMD)
	if len(es) != 3 {
		t.Fatalf("entries: %+v", es)
	}
	ub, rl, dg := es[0], es[1], es[2]
	if ub.ID != "ubwis" || ub.Kind != "experience" || len(ub.Bullets) != 2 {
		t.Errorf("ubwis: %+v", ub)
	}
	if rl.ID != "re-link" || rl.Title != "Re:Link - Peer-to-Peer Remote Access" || rl.Qualifiers[0] != "Honours Dissertation" {
		t.Errorf("re-link: %+v", rl)
	}
	if got := rl.Bullets[0].Text; !strings.HasSuffix(got, "encryption that wraps onto a second source line") {
		t.Errorf("wrapped line not joined: %q", got)
	}
	if len(dg.Bullets) != 1 || dg.Bullets[0].Render == nil || *dg.Bullets[0].Render {
		t.Errorf("digest: %+v", dg)
	}
	if ub.Bullets[0].Hash != Hash(ub.Bullets[0].Text) {
		t.Error("hash is not sha1 of the text")
	}
}

func TestStoreMissingFileIsEmptyAndSaveRoundTrips(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state", "cv-ledger.json")
	l, err := Load(p)
	if err != nil || len(l.Entries) != 0 {
		t.Fatalf("missing file: %v %+v", err, l)
	}
	if err := Save(p, &Ledger{CVHash: "x", Entries: []Entry{{ID: "a"}}}); err != nil {
		t.Fatal(err)
	}
	if l, _ = Load(p); l.CVHash != "x" || len(l.Entries) != 1 {
		t.Errorf("round trip: %+v", l)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".cv-ledger-*")); len(left) != 0 {
		t.Errorf("temp file left behind: %v", left)
	}
	os.WriteFile(p, []byte("{"), 0o644)
	if _, err := Load(p); err == nil {
		t.Error("corrupt ledger loaded without error")
	}
}

// Unchanged bullets keep their facts, approvals included, and are not sent;
// an edited bullet is; a bullet gone from cv.md drops; an approved inferred fact
// survives its bullet.
func TestBuildMergesByHash(t *testing.T) {
	ol, asked := fake(t, good)
	first, _, err := Build(context.Background(), ol, "sys", cvMD, digestMD, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 3 {
		t.Fatalf("first build made %d calls, want one per entry (3)", len(*asked))
	}
	ub := &first.Entries[0]
	ub.Bullets[0].Facts[0].Status = "approved"
	ub.Bullets[0].Facts[0].Text = "EDITED BY HAND"
	ub.Bullets[0].Framings = []Framing{{Phrase: "subscription billing", From: []string{ub.Bullets[0].Facts[0].ID}, Approved: true}}
	ub.Bullets[1].Facts = append(ub.Bullets[1].Facts, Fact{ID: "ubwis/2z", Text: "Used Kubernetes", Source: "inferred", Status: "approved"})

	edited := strings.Replace(cvMD, "from 8 minutes to 28 seconds with Redis", "from 8 minutes to 25 seconds with Redis", 1)
	*asked = nil
	next, st, err := Build(context.Background(), ol, "sys", edited, digestMD, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "25 seconds") || strings.Contains((*asked)[0], "Stripe") {
		t.Fatalf("expected one call with only the edited bullet, got %q", *asked)
	}
	if st.New != 1 || st.Reused != 3 || st.Dropped != 1 {
		t.Errorf("stats %+v", st)
	}
	got := next.Entries[0]
	if f := got.Bullets[0].Facts[0]; f.Text != "EDITED BY HAND" || f.Status != "approved" || len(got.Bullets[0].Framings) != 1 {
		t.Errorf("unchanged bullet lost its approvals: %+v", got.Bullets[0])
	}
	if len(got.Bullets) != 3 || got.Bullets[1].Facts[0].ID != "ubwis/3a" {
		t.Errorf("new bullet should take a fresh number, not reuse 2: %+v", got.Bullets[1])
	}
	hold := got.Bullets[2]
	if hold.Hash != InferredHash || len(hold.Facts) != 1 || hold.Facts[0].Text != "Used Kubernetes" {
		t.Errorf("approved inference did not survive its bullet: %+v", hold)
	}

	// Once more with nothing changed: no calls, and the holding bullet stays one.
	*asked = nil
	again, _, _ := Build(context.Background(), ol, "sys", edited, digestMD, next)
	if len(*asked) != 0 || len(again.Entries[0].Bullets) != 3 {
		t.Errorf("idempotent build: %d calls, %d bullets", len(*asked), len(again.Entries[0].Bullets))
	}
	if s := Diff(edited, digestMD, again); s.New != 0 || s.Dropped != 0 {
		t.Errorf("diff of a current ledger: %+v", s)
	}
}

func TestVerifyDropsFabrications(t *testing.T) {
	b := "Grew a members' club from 80 to 200 paying members using Stripe billing"
	got := verify(b, "Software Engineer - UBWIS", nil, []rawFact{
		{Text: "Grew membership from 80 to 200", Entities: []string{"stripe"}, Figures: []string{"80", "200"}},
		{Text: "Reached 500 members", Figures: []string{"500"}},          // figure not in bullet
		{Text: "Ran it on Kubernetes", Entities: []string{"Kubernetes"}}, // entity not in bullet
		{Text: "Worked at UBWIS", Entities: []string{"ubwis"}},           // entity in the title
		{Text: "grew membership from 80 to 200"},                         // duplicate
	})
	if len(got) != 2 || got[0].Text != "Grew membership from 80 to 200" || got[1].Entities[0] != "ubwis" {
		t.Fatalf("got %+v", got)
	}
	if !got[0].MustKeep {
		t.Error("a fact with figures must be must_keep")
	}
}

func TestVerifyAddsMissingFiguresToFirstFact(t *testing.T) {
	b := "Held 1080p60 under 80ms with AES-256-GCM and a 6-step saga, saving £85K+"
	got := verify(b, "Re:Link", nil, []rawFact{
		{Text: "Held 1080p60", Figures: []string{"1080p60"}},
		{Text: "Used a saga", Figures: []string{"10"}}, // a substring of 1080p60, which must not vouch for it
	})
	var all []string
	for _, f := range got {
		all = append(all, f.Figures...)
	}
	for _, want := range []string{"1080p60", "80ms", "AES-256-GCM", "6-step", "£85K"} {
		found := false
		for _, f := range all {
			found = found || key(f) == key(want)
		}
		if !found {
			t.Errorf("figure %q not covered; have %v", want, all)
		}
	}
}

func TestVerifyFallsBackToTheBulletItself(t *testing.T) {
	b := "Cut onboarding time by over 80%"
	for name, in := range map[string][]rawFact{
		"no facts":   nil,
		"all failed": {{Text: "Cut it by 90%", Figures: []string{"90%"}}, {Text: "x", Entities: []string{"Jira"}}},
	} {
		got := verify(b, "UBWIS", nil, in)
		if len(got) != 1 || got[0].Text != b || !got[0].MustKeep || len(got[0].Figures) != 1 || got[0].Figures[0] != "80%" {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestBuildBuildsEntityMap(t *testing.T) {
	ol, _ := fake(t, func(user string) any {
		return m{"bullets": []m{{"n": 1, "facts": []m{
			{"text": "Billed with Stripe", "entities": []string{"Stripe", "stripe"}, "figures": []string{}, "must_keep": false},
		}}}}
	})
	l, _, err := Build(context.Background(), ol, "sys", "**Experience**\n\n**Dev**\n\n**Acme** - X | 2024\n- Billed with Stripe and Redis\n", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if f := l.Entities["Stripe"]; len(f) != 2 {
		t.Errorf("entities %v", l.Entities)
	}
}

func TestTidyFiguresAndMustKeep(t *testing.T) {
	f := Fact{Text: "Sustained 12,000 orders/second at p99 under 200ms", MustKeep: true,
		Figures: []string{"12,000 orders/second", "200ms", "p99", "12,000", "k6", "a week"}}
	Tidy(&f, nil)
	if got := strings.Join(f.Figures, "|"); got != "12,000|200ms|p99|k6" {
		t.Errorf("figures = %s", got)
	}
	g := Fact{Text: "Verified performance in load tests", MustKeep: true}
	Tidy(&g, nil)
	if g.MustKeep {
		t.Error("blanket must_keep survived")
	}
	h := Fact{Text: "Tested at 250M+ simulated authentication events"}
	Tidy(&h, nil)
	if !h.MustKeep {
		t.Error("hedge word did not set must_keep")
	}
}
