package phase3

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// The corpus-relative specificity term, ported from cv-select.mjs's
// spikeBackground.
//
// Cosine alone answers "does this bullet match this posting". Subtracting the
// bullet's mean score across every past posting answers the more useful
// question: "does it match this one MORE than postings usually like it". A
// bullet that scores 0.55 against everything carries no signal about this
// offer; one that jumps to 0.55 from a 0.40 baseline does.
//
// Measured at +0.084 differentiator coverage, on a broad plateau (4 → +0.075,
// 6 → +0.084, 8 → +0.079), so the weight is not a knife edge.

// spikeMinOffers is the floor on how many past reports the background needs.
// Below it the mean is noise, and plain cosine is the better answer.
const spikeMinOffers = 20

type spikeCache struct {
	Hash   string    `json:"hash"`
	Offers int       `json:"offers"`
	Mean   []float64 `json:"mean"`
}

// spikeBackground returns each bullet's mean best-match score across every past
// posting's requirements, or nil when there is not enough corpus to be worth
// subtracting.
//
// nil is not an error: any failure here — too few reports, an unreadable cache,
// an embedder that will not answer — must leave the plain cosine scores
// standing rather than take the offer down.
func (t *Tailor) spikeBackground(ctx context.Context, bullets []string, fingerprint string) []float64 {
	// Held across the cache read as well as the rebuild: a worker that waits
	// here has to see the file the winner wrote, or it does the same minutes of
	// work over again for the same answer.
	t.spikeMu.Lock()
	defer t.spikeMu.Unlock()

	ents, err := os.ReadDir(t.L.Reports())
	if err != nil {
		return nil
	}
	var reports []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			reports = append(reports, e.Name())
		}
	}
	if len(reports) < spikeMinOffers {
		return nil
	}
	sort.Strings(reports)

	// The fingerprint is part of the key for the same reason cv-index.json uses
	// it: `ollama create snipe-embed` on a new base leaves the tag unchanged, so
	// a tag-keyed hash would keep stale means for ever.
	key, _ := json.Marshal(bullets)
	h := sha1.New()
	h.Write(key)
	h.Write([]byte("\n"))
	h.Write([]byte(strings.Join(reports, ",")))
	h.Write([]byte(fingerprint))
	hash := hex.EncodeToString(h.Sum(nil))

	if b, err := os.ReadFile(t.L.Spike()); err == nil {
		var c spikeCache
		if json.Unmarshal(b, &c) == nil && c.Hash == hash && len(c.Mean) == len(bullets) {
			return c.Mean
		}
	}

	// One requirement set per past report. A report with no parseable Block B
	// contributes nothing rather than a zero, which would drag every mean down.
	var reqSets [][]string
	for _, f := range reports {
		b, err := os.ReadFile(filepath.Join(t.L.Reports(), f))
		if err != nil {
			continue
		}
		if reqs := cvselect.ExtractBlockBRequirements(string(b)); len(reqs) > 0 {
			reqSets = append(reqSets, reqs)
		}
	}
	if len(reqSets) < spikeMinOffers {
		return nil
	}

	flat := append([]string{}, bullets...)
	for _, set := range reqSets {
		flat = append(flat, set...)
	}
	vecs, err := t.embed(ctx, flat)
	if err != nil || len(vecs) != len(flat) {
		return nil
	}
	bv := vecs[:len(bullets)]

	sums := make([]float64, len(bullets))
	at := len(bullets)
	for _, set := range reqSets {
		qv := vecs[at : at+len(set)]
		at += len(set)
		// Max over the set's requirements, matching how a live offer is scored.
		for i := range bullets {
			sums[i] += maxCosine(qv, bv[i])
		}
	}
	mean := make([]float64, len(bullets))
	for i, s := range sums {
		mean[i] = s / float64(len(reqSets))
	}

	if b, err := json.Marshal(spikeCache{Hash: hash, Offers: len(reqSets), Mean: mean}); err == nil {
		_ = state.WriteAtomic(t.L.Spike(), b) // a cache that cannot be written is not a run that should fail
	}
	fmt.Printf("  spike background rebuilt over %d offers\n", len(reqSets))
	return mean
}
