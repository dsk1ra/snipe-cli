// Package ollama is the whole model layer: schema-constrained chat, embeddings,
// and the call timing both of them record.
//
// It is stdlib http + json on purpose. The official github.com/ollama/ollama/api
// package drags the entire server module into the build for what is two
// endpoints and four fields.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultURL = "http://localhost:11434"

// Model names, built once from the Modelfiles (see prompts/Modelfile.*).
const (
	ModelScreen = "snipe-screen" // Phase 1 pre-score, Qwen3 4B
	ModelEval   = "snipe-eval"   // Phase 2 judgment + Phase 3 rerank, Qwen3 30B-A3B
	ModelEmbed  = "snipe-embed"  // Qwen3 Embedding 0.6B
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
	Timing  *TimingLog
	// Cold unloads the model before every chat, so each prompt is read from its
	// first token by a server that remembers nothing. A model that stays loaded
	// keeps the previous prompt and re-reads only what differs, and the server
	// also keeps finished prompts in memory and restores the one that shares
	// the longest prefix. The paths differ in floating point, and at
	// temperature 0 that is enough to change an answer: the Phase 3 judge
	// graded 6 of 12 postings differently from a byte-identical prompt on two
	// days. The gates set it under SNIPE_GATE_COLD so a number does not depend
	// on what the server read before. It costs one model load per call.
	Cold bool
}

func New(baseURL string, timing *TimingLog) *Client {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		// No client-level timeout: per-call deadlines come from the context, so
		// a cancelled run tears down an in-flight 30B call immediately. A fixed
		// Timeout here would fight that.
		HTTP:   &http.Client{},
		Timing: timing,
	}
}

// Options are the generation knobs. Temperature is 0 everywhere in production:
// at 0.1 the run-to-run noise floor on this stack was 0.091 with individual
// offers swinging 2.1 points, which makes a single-run A/B meaningless.
type Options struct {
	Temperature float64 `json:"temperature"`
	NumCtx      int     `json:"num_ctx,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
	NumGPU      int     `json:"num_gpu,omitempty"`
}

// Message is one turn of a chat. Request.Messages carries a few-shot history.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	Format   any       `json:"format,omitempty"`
	Options  Options   `json:"options"`
	// Think is a pointer so that only an explicit false is sent: every other
	// caller's body stays byte-identical to what the equivalence gate dumped.
	Think *bool `json:"think,omitempty"`
	// KeepAlive is a pointer for the same reason. Only the unload sets it.
	KeepAlive *int `json:"keep_alive,omitempty"`
}

type chatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	metrics
}

// metrics are the durations Ollama returns on every response. Recording them
// needs no stopwatch of our own, and they are more accurate than one: they
// exclude our JSON parsing and the model's queue wait.
type metrics struct {
	TotalDuration      int64  `json:"total_duration"`
	LoadDuration       int64  `json:"load_duration"`
	PromptEvalCount    int    `json:"prompt_eval_count"`
	PromptEvalDuration int64  `json:"prompt_eval_duration"`
	EvalCount          int    `json:"eval_count"`
	EvalDuration       int64  `json:"eval_duration"`
	DoneReason         string `json:"done_reason"`
}

// Chat is one schema-constrained call. schema is passed straight through as
// Ollama's `format`, which constrains decoding — the model cannot emit a shape
// that does not fit, so there is no repair loop.
//
// out must be a pointer; the reply is unmarshalled into it.
func (c *Client) Chat(ctx context.Context, req Request, out any) error {
	msgs := req.Messages
	if len(msgs) == 0 {
		msgs = []Message{{Role: "system", Content: req.System}, {Role: "user", Content: req.User}}
	}
	var think *bool
	if req.NoThink {
		off := false
		think = &off
	}
	body, err := json.Marshal(chatRequest{
		Model:    req.Model,
		Messages: msgs,
		Stream:   false,
		Format:   req.Schema,
		Options:  req.Options,
		Think:    think,
	})
	if err != nil {
		return err
	}

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	dumpRequest(req.Label, body)
	if c.Cold {
		// No messages and keep_alive 0 is Ollama's unload. A throwaway prompt
		// in between is not enough: the server saves the prompt it had and
		// restores it for the next call that shares its prefix. A failure here
		// is the real call's to report.
		zero := 0
		unload, _ := json.Marshal(chatRequest{Model: req.Model, Messages: []Message{}, KeepAlive: &zero})
		_ = c.post(ctx, "/api/chat", unload, &chatResponse{})
	}
	var resp chatResponse
	if err := c.post(ctx, "/api/chat", body, &resp); err != nil {
		return fmt.Errorf("%s: %w", req.Label, err)
	}
	c.Timing.log(req.Label, req.Model, req.ID, "", resp.metrics)

	if out == nil {
		return nil
	}
	raw, err := extractJSON(resp.Message.Content)
	if err != nil {
		return fmt.Errorf("%s: %w", req.Label, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: decoding %s: %w", req.Label, truncate(string(raw), 200), err)
	}
	return nil
}

// Request is one model call.
type Request struct {
	Model  string
	System string
	User   string
	// Messages, when set, is the whole conversation and System/User are ignored:
	// the Phase 3 judge sends its exemplars as user/assistant turns.
	Messages []Message
	Schema   any // JSON Schema; nil for free-form prose
	Options  Options
	// NoThink sends `think: false`. The Phase 3 rewrite sets it so a reasoning
	// model cannot spend the output cap before it writes a draft.
	NoThink bool
	Timeout time.Duration
	Label   string // the phase, for the timing log: "p1.score", "p2.judge", …
	ID      int    // the offer, for the timing log
}

// Text is Chat without a schema, for the one caller that wants prose.
func (c *Client) Text(ctx context.Context, req Request) (string, error) {
	// An empty system message is not "no system message": the template renders
	// it and it replaces the model's own default. bespoke-minicheck keeps its
	// instruction there, and with it blanked the model answered in prose.
	msgs := []Message{{Role: "user", Content: req.User}}
	if req.System != "" {
		msgs = []Message{{Role: "system", Content: req.System}, msgs[0]}
	}
	var think *bool
	if req.NoThink {
		off := false
		think = &off
	}
	body, err := json.Marshal(chatRequest{
		Model:    req.Model,
		Messages: msgs,
		Stream:   false,
		Options:  req.Options,
		Think:    think,
	})
	if err != nil {
		return "", err
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	var resp chatResponse
	if err := c.post(ctx, "/api/chat", body, &resp); err != nil {
		return "", fmt.Errorf("%s: %w", req.Label, err)
	}
	c.Timing.log(req.Label, req.Model, req.ID, "", resp.metrics)
	return resp.Message.Content, nil
}

type embedRequest struct {
	Model    string   `json:"model"`
	Input    []string `json:"input"`
	Truncate bool     `json:"truncate"`
	// Always sent, never omitted: Ollama serves a request that names no
	// num_gpu from whichever instance of the model is loaded.
	Options struct {
		NumGPU int `json:"num_gpu"`
	} `json:"options"`
}

// EmbedDevice is where the embedder runs, "cpu" or "gpu".
//
// Only one model fits in 6 GB of VRAM. On the GPU the embedder evicts the 30B
// every time it is called, 12 to 26 s to swap in and 6 s more for the next chat
// call to load the 30B back, some 45 s an offer. On the CPU it loads in a
// second beside the 30B and embeds about 16 short texts a second against 70,
// which a dozen requirements never notice.
//
// The CPU also returns the same vector for the same text every time, which the
// GPU does not, and the two disagree enough to pick a different top three for
// one requirement in seven. So it is one device for everything, and the device
// is part of embed.Indexer.Fingerprint: changing it rebuilds every cache of
// vectors rather than comparing across the two.
const EmbedDevice = "cpu"

type embedResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
	metrics
}

// Embed vectorises a batch. Batching matters: the index build is a few hundred
// texts and one call per text would spend its whole time in HTTP.
func (c *Client) Embed(ctx context.Context, texts []string, model string) ([][]float64, error) {
	if model == "" {
		model = ModelEmbed
	}
	if len(texts) == 0 {
		return nil, nil
	}
	req := embedRequest{Model: model, Input: texts, Truncate: true}
	if EmbedDevice == "gpu" {
		req.Options.NumGPU = 99 // above any layer count: offload everything
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	var resp embedResponse
	if err := c.post(ctx, "/api/embed", body, &resp); err != nil {
		return nil, err
	}
	c.Timing.log("embed", model, 0, fmt.Sprintf("n=%d", len(texts)), resp.metrics)
	if len(resp.Embeddings) == 0 {
		return nil, fmt.Errorf("ollama embed returned no embeddings")
	}
	return resp.Embeddings, nil
}

// dumpDir, when SNIPE_DUMP is set, writes every request body to a file named
// after its label. It exists for the equivalence gate: when two implementations
// produce different prose from the same model at temperature 0, the difference
// is in the request, and guessing at it costs a 30B call per guess.
func dumpRequest(label string, body []byte) {
	dir := os.Getenv("SNIPE_DUMP")
	if dir == "" || label == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, label+".json"), body, 0o644)
}

func (c *Client) post(ctx context.Context, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, truncate(string(b), 200))
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// Up reports whether the server answers, and which models it has. Called once
// per run so a missing model fails before the first offer rather than during it.
func (c *Client) Up(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama not reachable at %s: %w", c.BaseURL, err)
	}
	defer res.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(res.Body).Decode(&tags); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// extractJSON recovers the object from a reply. Schema-constrained decoding
// should make this unnecessary, but a model that ignores its schema wraps the
// object in a fence or prefixes prose, and losing a whole 30B call to that is
// not worth the purity.
func extractJSON(raw string) ([]byte, error) {
	s := strings.TrimSpace(raw)
	if json.Valid([]byte(s)) {
		return []byte(s), nil
	}
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		if j := strings.Index(rest, "```"); j >= 0 {
			if inner := strings.TrimSpace(rest[:j]); json.Valid([]byte(inner)) {
				return []byte(inner), nil
			}
		}
	}
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		if inner := s[start : end+1]; json.Valid([]byte(inner)) {
			return []byte(inner), nil
		}
	}
	return nil, fmt.Errorf("no JSON in model response: %s", truncate(s, 300))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Show fetches a model's metadata. Used to fingerprint the embedder: the tag is
// stable across rebuilds, so only the resolved base identifies what actually
// produced a set of vectors.
func (c *Client) Show(ctx context.Context, model string, out any) error {
	body, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.post(ctx, "/api/show", body, out)
}
