package minicheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/ollama"
)

func fake(t *testing.T, reply string, got *map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			_ = json.NewDecoder(r.Body).Decode(got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": reply}})
	}))
	t.Cleanup(srv.Close)
	return &Client{OL: ollama.New(srv.URL, nil)}
}

func TestCheckVerdicts(t *testing.T) {
	for reply, want := range map[string]bool{"Yes": true, " yes\n": true, "YES.": true, "No": false, "no": false} {
		got, err := fake(t, reply, nil).Check(context.Background(), "d", "c")
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v; want %v", reply, got, err, want)
		}
	}
}

func TestCheckUnreadableIsError(t *testing.T) {
	for _, reply := range []string{"", "Maybe", "Yes, because", "I think so"} {
		if ok, err := fake(t, reply, nil).Check(context.Background(), "d", "c"); err == nil || ok {
			t.Errorf("%q: got %v, %v; want an error and not supported", reply, ok, err)
		}
	}
}

func TestCheckRequest(t *testing.T) {
	var req map[string]any
	if _, err := fake(t, "Yes", &req).Check(context.Background(), "the doc", "the claim"); err != nil {
		t.Fatal(err)
	}
	if req["model"] != Model {
		t.Errorf("model = %v", req["model"])
	}
	msgs := req["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)["content"]
	if last != "Document: the doc\nClaim: the claim" {
		t.Errorf("prompt = %q", last)
	}
	opts := req["options"].(map[string]any)
	if opts["temperature"] != 0.0 || opts["num_ctx"] == nil || opts["num_predict"] == nil {
		t.Errorf("options = %v", opts)
	}
}
