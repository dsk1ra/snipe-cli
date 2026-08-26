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
	ModelCV     = "snipe-cv"     // Phase 3 tailor, Qwen2.5 7B Coder
	ModelEmbed  = "snipe-embed"  // Qwen3 Embedding 0.6B
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
	Timing  *TimingLog
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

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
	Format   any       `json:"format,omitempty"`
	Options  Options   `json:"options"`
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
	body, err := json.Marshal(chatRequest{
		Model: req.Model,
		Messages: []message{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
		Stream:  false,
		Format:  req.Schema,
		Options: req.Options,
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
	Model   string
	System  string
	User    string
	Schema  any // JSON Schema; nil for free-form prose
	Options Options
	Timeout time.Duration
	Label   string // the phase, for the timing log: "p1.score", "p2.judge", …
	ID      int    // the offer, for the timing log
}

// Text is Chat without a schema, for the one caller that wants prose.
func (c *Client) Text(ctx context.Context, req Request) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:    req.Model,
		Messages: []message{{Role: "system", Content: req.System}, {Role: "user", Content: req.User}},
		Stream:   false,
		Options:  req.Options,
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
}

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
	body, err := json.Marshal(embedRequest{Model: model, Input: texts, Truncate: true})
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
