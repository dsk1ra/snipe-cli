package extract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/ollama"
)

const posting = `Senior Backend Engineer

Location: Central London.  We work hybrid, 3 days a week
in the office. You'll need 5+ years of experience.
Fluent German is required; French is a plus.
Stack: Django and Go, with AWS.`

func reply(t *testing.T, js string) Reply {
	t.Helper()
	var r Reply
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestVerifyKeepsQuotedFacts(t *testing.T) {
	r := reply(t, `{
	 "attendance": {"quote": "hybrid, 3 days a week in the office", "mode": "hybrid", "days_per_week": 3, "days_per_month": 0, "place": "Central London"},
	 "languages": [
	   {"quote": "Fluent German is required", "language": "German", "requirement": "required"},
	   {"quote": "French is a plus", "language": "French", "requirement": "preferred"},
	   {"quote": "Stack: Django and Go", "language": "Go", "requirement": "required"}],
	 "years": {"quote": "5+ years of experience", "min_years": 5},
	 "stack": [
	   {"quote": "Django and Go, with AWS", "term": "Django", "language": "python", "requirement": "required"},
	   {"quote": "Django and Go, with AWS", "term": "AWS", "language": "none", "requirement": "required"},
	   {"quote": "Fluent German is required", "term": "Go", "language": "go", "requirement": "required"}]}`)
	f := Verify(r, posting)
	a := f.Attendance
	// The posting breaks the line mid-quote and doubles a space; the quote has neither.
	if !a.QuoteOK || a.Mode != Hybrid || a.DaysPerWeek != 3 || a.Place != "Central London" {
		t.Errorf("attendance %+v", a)
	}
	if len(f.Languages) != 2 || f.LanguagesClaimed != 3 || f.LanguagesOK != 2 { // Go is a programming language
		t.Errorf("languages %+v", f.Languages)
	}
	if !f.Years.QuoteOK || f.Years.MinYears != 5 {
		t.Errorf("years %+v", f.Years)
	}
	// A term that is no language, and a quote that does not name its term, drop out.
	if len(f.Stack) != 1 || f.Stack[0].Language != "python" || f.Stack[0].Term != "Django" {
		t.Errorf("stack %+v", f.Stack)
	}
}

func TestVerifyDiscardsWhatTheQuoteDoesNotSupport(t *testing.T) {
	r := reply(t, `{
	 "attendance": {"quote": "fully remote across the UK", "mode": "remote", "days_per_week": 0, "days_per_month": 0, "place": "Leeds"},
	 "languages": [
	   {"quote": "Fluent German is required", "language": "French", "requirement": "required"},
	   {"quote": "Fluent Italian is required", "language": "Italian", "requirement": "required"}],
	 "years": {"quote": "5+ years of experience", "min_years": 8},
	 "stack": [{"quote": "Rust and Kubernetes", "term": "Rust", "language": "rust", "requirement": "required"}]}`)
	f := Verify(r, posting)
	if a := f.Attendance; a.QuoteOK || !a.Claimed || a.Mode != Unspecified || a.Place != "" || a.DaysPerWeek != 0 {
		t.Errorf("an invented attendance quote left %+v", a)
	}
	if len(f.Languages) != 0 || f.LanguagesClaimed != 2 || f.LanguagesOK != 0 {
		t.Errorf("a quote that does not name the language, and one not in the posting, kept %+v", f.Languages)
	}
	if f.Years.QuoteOK || f.Years.MinYears != 0 || !f.Years.Claimed {
		t.Errorf("8 years from a quote saying 5+ kept %+v", f.Years)
	}
	if len(f.Stack) != 0 {
		t.Errorf("a stack quote not in the posting kept %+v", f.Stack)
	}
}

func TestVerifyPlaceMustBeInThePosting(t *testing.T) {
	r := reply(t, `{"attendance": {"quote": "3 days a week in the office", "mode": "hybrid", "days_per_week": 3, "place": "Manchester"}}`)
	a := Verify(r, posting).Attendance
	if !a.QuoteOK || a.Place != "" || a.DaysPerWeek != 3 {
		t.Errorf("an invented place should clear only the place: %+v", a)
	}
}

func TestVerifyNothingClaimed(t *testing.T) {
	f := Verify(reply(t, `{"attendance": {"mode": "unspecified"}}`), posting)
	if f.Attendance.Claimed || f.Years.Claimed || len(f.Stack) != 0 || f.LanguagesClaimed != 0 {
		t.Errorf("claimed %+v", f)
	}
	if f.Attendance.Mode != Unspecified {
		t.Errorf("unknowns must read unspecified: %+v", f)
	}
}

func TestNormFoldsTypography(t *testing.T) {
	if Norm("You’ll  need\n5–7 YEARS") != "you'll need 5-7 years" {
		t.Errorf("got %q", Norm("You’ll  need\n5–7 YEARS"))
	}
}

func TestHasNumber(t *testing.T) {
	for q, n := range map[string]int{"5+ years": 5, "5-7 years": 7, "five years": 5, "10 years": 10} {
		if !hasNumber(q, n) {
			t.Errorf("%q should carry %d", q, n)
		}
	}
	for q, n := range map[string]int{"10 years": 1, "15 years": 5, "5-7 years": 6} {
		if hasNumber(q, n) {
			t.Errorf("%q should not carry %d", q, n)
		}
	}
}

// Every string is length-bounded and every array item-bounded, or the 4B can
// run on to the context limit inside a quote.
func TestSchemaIsBounded(t *testing.T) {
	b, err := json.Marshal(Schema())
	if err != nil {
		t.Fatal(err)
	}
	var root any
	_ = json.Unmarshal(b, &root)
	walkTyped(t, "", root)
	for _, k := range []string{`"maxLength"`, `"maxItems"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("schema has no %s", k)
		}
	}
}

func walkTyped(t *testing.T, path string, v any) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	switch m["type"] {
	case "string":
		if _, enum := m["enum"]; !enum && m["maxLength"] == nil {
			t.Errorf("%s: unbounded string", path)
		}
	case "array":
		if m["maxItems"] == nil {
			t.Errorf("%s: unbounded array", path)
		}
		walkTyped(t, path+"[]", m["items"])
	case "integer":
		if m["minimum"] == nil || m["maximum"] == nil {
			t.Errorf("%s: unbounded integer", path)
		}
	case "object":
		for k, c := range m["properties"].(map[string]any) {
			walkTyped(t, path+"."+k, c)
		}
	}
}

func fakeServer(t *testing.T, calls *int, content string) *ollama.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": content}})
	}))
	t.Cleanup(srv.Close)
	return ollama.New(srv.URL, nil)
}

const fakeReply = `{"attendance": {"quote": "hybrid, 3 days a week in the office", "mode": "hybrid", "days_per_week": 3, "days_per_month": 0, "place": "Central London"},
 "languages": [], "years": {"quote": "", "min_years": 0}, "stack": []}`

func TestCacheHitMakesNoSecondCall(t *testing.T) {
	dir := t.TempDir()
	var calls int
	ol := fakeServer(t, &calls, fakeReply)
	ctx := context.Background()

	e := New(ol, dir, "snipe-screen", "system v1")
	f, err := e.Extract(ctx, posting)
	if err != nil || f.Attendance.Mode != Hybrid {
		t.Fatalf("%v %+v", err, f)
	}
	if _, err := e.Extract(ctx, posting); err != nil || calls != 1 {
		t.Errorf("second extract of the same posting: %d calls, err %v", calls, err)
	}
	// A new process reads the file.
	e2 := New(ol, dir, "snipe-screen", "system v1")
	if _, err := e2.Extract(ctx, posting); err != nil || calls != 1 || e2.Calls != 0 {
		t.Errorf("after reload: %d calls, err %v", calls, err)
	}
	// A different posting, a different prompt and a different model each miss.
	if _, err := e2.Extract(ctx, posting+" Also: free lunch."); err != nil || calls != 2 {
		t.Errorf("a changed posting should call: %d", calls)
	}
	if _, err := New(ol, dir, "snipe-screen", "system v2").Extract(ctx, posting); err != nil || calls != 3 {
		t.Errorf("a changed prompt should call: %d", calls)
	}
	if _, err := New(ol, dir, "other-model", "system v1").Extract(ctx, posting); err != nil || calls != 4 {
		t.Errorf("a changed model should call: %d", calls)
	}
}

func TestExtractWithoutClientAnswersFromCacheOnly(t *testing.T) {
	if _, err := New(nil, t.TempDir(), "", "s").Extract(context.Background(), posting); err == nil {
		t.Error("a cold cache with no client must say so")
	}
}

func TestCVLanguages(t *testing.T) {
	var calls int
	ol := fakeServer(t, &calls, `{"terms": [
	  {"term": "Spring Boot", "language": "java"},
	  {"term": "Terraform", "language": "none"},
	  {"term": "Rust", "language": "rust"}]}`)
	dir := t.TempDir()
	mk := func(ents ...string) *Extractor {
		e := New(ol, dir, "snipe-screen", "sys")
		e.CVSystem, e.Entities = "cv sys", ents
		return e
	}
	e := mk("Spring Boot", "Terraform")
	got, err := e.CVLanguages(context.Background())
	// Rust is not in the list it was given, and none is not a language.
	if err != nil || len(got) != 1 || got[0] != "java" || calls != 1 {
		t.Fatalf("%v %v %d", got, err, calls)
	}
	if l, ok := mk("Spring Boot", "Terraform").CVLanguagesCached(); !ok || len(l) != 1 || calls != 1 {
		t.Errorf("cached read: %v %v %d", l, ok, calls)
	}
	if _, ok := mk("Spring Boot", "Terraform", "Kafka").CVLanguagesCached(); ok {
		t.Error("a changed entity list hit the cache")
	}
	if _, err := mk().CVLanguages(context.Background()); err == nil {
		t.Error("no entities should be an error, so the caller falls back")
	}
}
