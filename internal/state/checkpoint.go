package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Checkpoint records what an offer has already produced, stage by stage, so a
// stopped or crashed run resumes at the next stage instead of the start of the
// phase.
//
// This is the thing the bash runner could not do. Its state was phase-granular,
// so a kill during Phase 2 discarded all three staged model calls, and a kill
// during Phase 3 discarded the selection *and* the 66-second judge rerank —
// the two most expensive calls in the pipeline.
//
// Stages are keyed by name and guarded by a hash of their input, which gets
// cascading invalidation for free: stage N+1 takes stage N's output as input,
// so if N re-runs and produces something different, N+1's hash no longer
// matches and it re-runs too. The one rule a caller must follow is that a
// stage's `in` argument must contain everything the stage reads. A stage that
// silently re-reads a file off disk is not covered — pass the file's content or
// its hash in.
type Checkpoint struct {
	path   string
	mu     sync.Mutex
	Stages map[string]json.RawMessage `json:"stages"`
	Inputs map[string]string          `json:"inputs"`

	// Running is the stage in flight, written before the call and cleared in
	// the same save that stores its output.
	//
	// It is the only record of where a run was when it stopped. A stage that
	// never lands writes nothing else — that is the whole point of the design —
	// so without this a paused offer looks exactly like one that had simply got
	// that far and no further, and neither the resume nor the UI could name the
	// call that was thrown away.
	Running string `json:"running,omitempty"`
}

// LoadCheckpoint opens the record for one offer. A missing or corrupt file is
// an empty checkpoint: the worst case is redoing work, never refusing to run.
func LoadCheckpoint(dir string, id int) *Checkpoint {
	c := &Checkpoint{
		path:   filepath.Join(dir, fmt.Sprintf("%d.json", id)),
		Stages: map[string]json.RawMessage{},
		Inputs: map[string]string{},
	}
	b, err := os.ReadFile(c.path)
	if err != nil {
		return c
	}
	var got Checkpoint
	if json.Unmarshal(b, &got) != nil {
		return c
	}
	if got.Stages != nil {
		c.Stages = got.Stages
	}
	if got.Inputs != nil {
		c.Inputs = got.Inputs
	}
	c.Running = got.Running
	return c
}

// Discard drops the whole record — a full retry from Phase 1.
func (c *Checkpoint) Discard() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Stages = map[string]json.RawMessage{}
	c.Inputs = map[string]string{}
	c.Running = ""
	err := os.Remove(c.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Done lists the stages currently cached, for the resume line. Sorted, because
// a map's key order is random and a line that reads "p2.evidence, p2.jdparse"
// one run and the reverse the next reads as though something moved.
func (c *Checkpoint) Done() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.Stages))
	for k := range c.Stages {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Interrupted is the stage that was in flight when the run stopped, empty when
// the offer stopped cleanly between stages.
//
// Pause, crash and kill are indistinguishable here, and deliberately so: the
// answer to all three is the same, which is to run exactly that stage again.
func (c *Checkpoint) Interrupted() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Running
}

// begin marks a stage in flight. It costs one small atomic write per stage, on
// calls that take seconds to minutes.
func (c *Checkpoint) begin(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Running = name
	return c.save()
}

func (c *Checkpoint) save() error {
	b, err := json.MarshalIndent(struct {
		Stages  map[string]json.RawMessage `json:"stages"`
		Inputs  map[string]string          `json:"inputs"`
		Running string                     `json:"running,omitempty"`
	}{c.Stages, c.Inputs, c.Running}, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(c.path, b)
}

func hashOf(v any) (string, error) {
	// json.Marshal is deterministic for a given value: struct fields in
	// declaration order, map keys sorted. That is what makes this a stable key.
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Staged runs fn unless its cached output for this exact input is already on
// disk, in which case it returns that and makes no call.
//
// It is a free function rather than a method because Go does not allow type
// parameters on methods.
func Staged[I, O any](ctx context.Context, c *Checkpoint, name string, in I, fn func(context.Context, I) (O, error)) (O, error) {
	var zero O
	h, err := hashOf(in)
	if err != nil {
		return zero, fmt.Errorf("%s: hashing input: %w", name, err)
	}

	c.mu.Lock()
	cached, hasOut := c.Stages[name]
	matches := c.Inputs[name] == h
	c.mu.Unlock()

	if hasOut && matches {
		var out O
		if json.Unmarshal(cached, &out) == nil {
			return out, nil
		}
		// A cached value that no longer fits its type means the struct changed
		// under it. Fall through and recompute rather than fail the offer.
	}

	// Marked before the call, not after: a pause cancels ctx mid-call and the
	// stage writes nothing, so this is the only trace of what was dropped.
	if err := c.begin(name); err != nil {
		return zero, fmt.Errorf("%s: marking %s in flight: %w", name, name, err)
	}

	out, err := fn(ctx, in)
	if err != nil {
		// Left marked. Cancelled or failed, the call did not land, and the mark
		// is what tells the resume to run this stage rather than the next one.
		return zero, err
	}

	b, err := json.Marshal(out)
	if err != nil {
		return zero, fmt.Errorf("%s: encoding output: %w", name, err)
	}
	c.mu.Lock()
	c.Stages[name] = b
	c.Inputs[name] = h
	c.Running = "" // it landed, in the same write that stores it
	err = c.save()
	c.mu.Unlock()
	if err != nil {
		return zero, fmt.Errorf("%s: saving checkpoint: %w", name, err)
	}
	return out, nil
}

// Work is one offer's interrupted stage.
type Work struct {
	ID    int
	Stage string
}

// Interrupted lists every offer whose checkpoint names a stage in flight, in id
// order.
//
// With no run holding the lock this IS the paused state: nothing else is
// recorded, and nothing else needs to be. A stage marked here and no process to
// finish it means the same thing however the run ended.
func Interrupted(workDir string) []Work {
	ents, err := os.ReadDir(workDir)
	if err != nil {
		return nil
	}
	var out []Work
	for _, e := range ents {
		var id int
		if _, err := fmt.Sscanf(e.Name(), "%d.json", &id); err != nil {
			continue
		}
		if stage := LoadCheckpoint(workDir, id).Interrupted(); stage != "" {
			out = append(out, Work{ID: id, Stage: stage})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
