package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Cold puts one throwaway call in front of the real one, on the same model and
// window so that nothing reloads, and capped at one token.
func TestColdFlushesThePromptCacheFirst(t *testing.T) {
	var seen []chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		seen = append(seen, req)
		_, _ = w.Write([]byte(`{"message":{"content":"{}"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	c.Cold = true
	var out map[string]any
	req := Request{Model: "m", System: "the real system prompt", User: "u", Options: Options{NumCtx: 8192, NumPredict: 900}}
	if err := c.Chat(context.Background(), req, &out); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("%d requests, want the flush and the call", len(seen))
	}
	f := seen[0]
	if f.Model != "m" || f.Options.NumCtx != 8192 || f.Options.NumPredict != 1 || f.Messages[0].Content == req.System {
		t.Errorf("flush %+v: want the same model and window, one token, another prompt", f)
	}
	if seen[1].Messages[0].Content != req.System || seen[1].Options.NumPredict != 900 {
		t.Errorf("the real call changed: %+v", seen[1])
	}

	c.Cold = false
	seen = nil
	if err := c.Chat(context.Background(), req, &out); err != nil || len(seen) != 1 {
		t.Errorf("without Cold: %d requests, err %v", len(seen), err)
	}
}
