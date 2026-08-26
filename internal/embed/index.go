package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

// Cosine similarity between two vectors.
//
// A dimension mismatch means the index was built by a different embedder, and
// it is fatal rather than a zero. Without the guard the loop reads past the
// shorter vector, the dot product goes NaN, and a "0 on NaN" fallback silently
// returns 0 for every atom — top-k then ranks an all-zero list, retrieval
// degrades to "the first k atoms", and nothing errors.
func Cosine(a, b []float64) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("embedding dim mismatch: %d vs %d — stale index, delete batch/cache/cv-index.json and batch/cache/jd-index.json", len(a), len(b))
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	d := math.Sqrt(na) * math.Sqrt(nb)
	if d == 0 {
		return 0, nil
	}
	return dot / d, nil
}

// Scored is an atom with its similarity to a query.
type Scored struct {
	Atom
	Sim float64
}

// TopK ranks the index against a query vector.
func TopK(query []float64, index []Atom, k int) ([]Scored, error) {
	out := make([]Scored, 0, len(index))
	for _, a := range index {
		sim, err := Cosine(query, a.Vec)
		if err != nil {
			return nil, err
		}
		out = append(out, Scored{Atom: a, Sim: sim})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sim > out[j].Sim })
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// Indexer builds and caches the vector indexes.
type Indexer struct {
	OL      *ollama.Client
	Model   string
	CVPath  string // batch/cv-index.json
	JDPath  string // batch/jd-index.json
	JDsDir  string // batch/jds
	mu      sync.Mutex
	fpCache string
}

// Fingerprint identifies what actually produced the vectors.
//
// The tag (snipe-embed) is stable by design, so rebuilding the model on a new
// base leaves the name — and therefore any name-keyed hash — unchanged, while
// silently keeping a stale index. The resolved base is what has to key it.
func (ix *Indexer) Fingerprint(ctx context.Context) string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.fpCache != "" {
		return ix.fpCache
	}
	model := ix.model()
	fp := model
	var show struct {
		Details struct {
			ParentModel   string `json:"parent_model"`
			ParameterSize string `json:"parameter_size"`
			Quantization  string `json:"quantization_level"`
		} `json:"details"`
	}
	if err := ix.OL.Show(ctx, model, &show); err == nil {
		parent := show.Details.ParentModel
		if parent == "" {
			// Empty for a bare base pulled without a Modelfile. Fall back to the
			// tag so the key stays stable rather than collapsing to "||".
			parent = model
		}
		fp = strings.Join([]string{model, parent, show.Details.ParameterSize, show.Details.Quantization}, "|")
	}
	// An offline or older Ollama degrades to the tag, which is the old behaviour.
	ix.fpCache = fp
	return fp
}

func (ix *Indexer) model() string {
	if ix.Model != "" {
		return ix.Model
	}
	return ollama.ModelEmbed
}

func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

type cvIndexFile struct {
	Hash    string `json:"hash"`
	Model   string `json:"model"`
	BuiltAt string `json:"built_at"`
	Atoms   []Atom `json:"atoms"`
}

// LoadCV returns the embedded CV atoms, rebuilding when the CV text or the
// embedder has changed.
func (ix *Indexer) LoadCV(ctx context.Context, atoms []Atom) ([]Atom, error) {
	texts := make([]string, len(atoms))
	for i, a := range atoms {
		texts[i] = a.Text
	}
	keyJSON, err := json.Marshal(texts)
	if err != nil {
		return nil, err
	}
	want := hashOf(string(keyJSON), ix.Fingerprint(ctx))

	if b, err := os.ReadFile(ix.CVPath); err == nil {
		var cached cvIndexFile
		if json.Unmarshal(b, &cached) == nil && cached.Hash == want {
			return cached.Atoms, nil
		}
	}

	vecs, err := Batch(ctx, ix.OL, texts, ix.model())
	if err != nil {
		return nil, err
	}
	out := make([]Atom, len(atoms))
	for i := range atoms {
		out[i] = atoms[i]
		out[i].Vec = vecs[i]
	}
	b, err := json.Marshal(cvIndexFile{Hash: want, Model: ix.model(), BuiltAt: nowISO(), Atoms: out})
	if err != nil {
		return nil, err
	}
	return out, state.WriteAtomic(ix.CVPath, b)
}

// JDEntry is one past posting's vector, for calibration retrieval.
type JDEntry struct {
	ID  string    `json:"id"`
	Vec []float64 `json:"vec"`
}

type jdIndexFile struct {
	Model   string    `json:"model"`
	Entries []JDEntry `json:"entries"`
}

// embedBatch keeps request sizes sane. The index build is a few hundred texts;
// one call each would spend its whole time in HTTP.
const embedBatch = 32

// Batch splits an embed call into fixed-size requests and checks that the
// embedder answered every one of them.
//
// One call per text spends its whole time in HTTP. One call for everything is
// the other failure: the Phase 3 spike background asks for every CV bullet plus
// every requirement of every past report, which reached 3,037 texts in a single
// request and 53 seconds of wall clock — and, on two Phase 3 workers, twice at
// once. The index build already batched and the spike did not, so the batching
// lives here where both reach it.
//
// The length check is the same one: a short response used to be a silent
// vecs[j] index panic on one path and a clean error on the other.
func Batch(ctx context.Context, ol *ollama.Client, texts []string, model string) ([][]float64, error) {
	out := make([][]float64, 0, len(texts))
	for i := 0; i < len(texts); i += embedBatch {
		chunk := texts[i:min(i+embedBatch, len(texts))]
		vecs, err := ol.Embed(ctx, chunk, model)
		if err != nil {
			return nil, err
		}
		if len(vecs) != len(chunk) {
			return nil, fmt.Errorf("embedder returned %d vectors for %d texts", len(vecs), len(chunk))
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// LoadJD returns the past-JD index, embedding only the ids it does not have.
//
// A fingerprint mismatch discards the lot rather than topping it up: a partial
// re-embed would mix two vector spaces, and cosine across them is meaningless
// without being wrong in any way a caller could detect.
func (ix *Indexer) LoadJD(ctx context.Context) ([]JDEntry, error) {
	fp := ix.Fingerprint(ctx)
	cached := jdIndexFile{Model: fp}
	if b, err := os.ReadFile(ix.JDPath); err == nil {
		var got jdIndexFile
		if json.Unmarshal(b, &got) == nil && got.Model == fp {
			cached = got
		}
	}
	have := map[string]bool{}
	for _, e := range cached.Entries {
		have[e.ID] = true
	}

	ents, err := os.ReadDir(ix.JDsDir)
	if err != nil {
		return cached.Entries, nil // no corpus yet is not an error
	}
	var fresh []string
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".txt")
		if !have[id] {
			fresh = append(fresh, id)
		}
	}
	sort.Strings(fresh)
	if len(fresh) == 0 {
		return cached.Entries, nil
	}

	texts := make([]string, len(fresh))
	for i, id := range fresh {
		b, err := os.ReadFile(filepath.Join(ix.JDsDir, id+".txt"))
		if err != nil {
			return nil, err
		}
		texts[i] = text.CleanJD(string(b), 2000)
	}
	vecs, err := Batch(ctx, ix.OL, texts, ix.model())
	if err != nil {
		return nil, err
	}
	for i, id := range fresh {
		cached.Entries = append(cached.Entries, JDEntry{ID: id, Vec: vecs[i]})
	}
	b, err := json.Marshal(cached)
	if err != nil {
		return nil, err
	}
	return cached.Entries, state.WriteAtomic(ix.JDPath, b)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
