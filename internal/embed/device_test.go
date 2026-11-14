package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/ollama"
)

// Every embed request must name its device, and the fingerprint must carry it,
// so vectors from the CPU and the GPU never share a cache.
func TestEmbedNamesItsDevice(t *testing.T) {
	var asked []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input   []string       `json:"input"`
			Options map[string]any `json:"options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path == "/api/embed" {
			asked = append(asked, body.Options["num_gpu"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": make([][]float64, len(body.Input))})
	}))
	defer srv.Close()
	ol := ollama.New(srv.URL, nil)

	if _, err := Batch(context.Background(), ol, make([]string, embedBatch+1), ""); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"cpu": 0, "gpu": 99}[ollama.EmbedDevice]
	if len(asked) != 2 || asked[0] != want || asked[1] != want {
		t.Errorf("num_gpu per request %v, want %v on both", asked, want)
	}
	if fp := (&Indexer{OL: ol}).Fingerprint(context.Background()); !strings.HasSuffix(fp, "|"+ollama.EmbedDevice) {
		t.Errorf("fingerprint %q does not end in the device", fp)
	}
}
