package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Cold puts Ollama's unload in front of the real call: the same model, no
// messages and keep_alive 0. The real call is sent as it would be without it.
func TestColdUnloadsTheModelFirst(t *testing.T) {
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
		t.Fatalf("%d requests, want the unload and the call", len(seen))
	}
	u := seen[0]
	if u.Model != "m" || len(u.Messages) != 0 || u.KeepAlive == nil || *u.KeepAlive != 0 {
		t.Errorf("unload %+v: want the same model, no messages, keep_alive 0", u)
	}
	if seen[1].Messages[0].Content != req.System || seen[1].Options.NumPredict != 900 || seen[1].KeepAlive != nil {
		t.Errorf("the real call changed: %+v", seen[1])
	}

	c.Cold = false
	seen = nil
	if err := c.Chat(context.Background(), req, &out); err != nil || len(seen) != 1 {
		t.Errorf("without Cold: %d requests, err %v", len(seen), err)
	}
}
