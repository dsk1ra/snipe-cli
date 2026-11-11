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

// spikeCache keeps one row per past report: each bullet's best cosine against
// that report's requirements. The background is the mean of the rows.
//
// It used to hold only the mean, keyed on the list of report files, so every
// new report threw it away. Phase 2 writes a report just before Phase 3 runs,
// which made every single-offer run embed all of them again: 4,037 texts and
// 67 s on 404 reports, more than the judge call. A row depends on the bullets,
// the embedder and its own report, so a new report costs one row.
type spikeCache struct {
	Key  string               `json:"key"`  // bullets and embedder
	Rows map[string][]float64 `json:"rows"` // by report file; empty for a report with no Block B
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
	// a tag-keyed hash would keep stale rows for ever.
	key, _ := json.Marshal(bullets)
	h := sha1.New()
	h.Write(key)
	h.Write([]byte("\n"))
	h.Write([]byte(fingerprint))
	cache := spikeCache{Key: hex.EncodeToString(h.Sum(nil)), Rows: map[string][]float64{}}
	if b, err := os.ReadFile(t.L.Spike()); err == nil {
		var c spikeCache
		if json.Unmarshal(b, &c) == nil && c.Key == cache.Key && c.Rows != nil {
			cache = c
		}
	}

	// One requirement set per report the cache has not seen.
	var fresh []string
	var reqSets [][]string
	for _, f := range reports {
		if _, ok := cache.Rows[f]; ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(t.L.Reports(), f))
		if err != nil {
			continue
		}
		fresh = append(fresh, f)
		reqSets = append(reqSets, cvselect.ExtractBlockBRequirements(string(b)))
	}
	if len(fresh) > 0 {
		flat := append([]string{}, bullets...)
		for _, set := range reqSets {
			flat = append(flat, set...)
		}
		var vecs [][]float64
		if len(flat) > len(bullets) { // a report with no Block B needs no embedding
			var err error
			if vecs, err = t.embed(ctx, flat); err != nil || len(vecs) != len(flat) {
				return nil
			}
		}
		at := len(bullets)
		for k, set := range reqSets {
			// A report with no parseable Block B keeps an empty row, so it is not
			// read again and adds nothing to the mean. A zero would drag it down.
			row := []float64{}
			if len(set) > 0 {
				qv := vecs[at : at+len(set)]
				at += len(set)
				// Max over the set's requirements, matching how a live offer is scored.
				row = make([]float64, len(bullets))
				for i := range bullets {
					row[i] = maxCosine(qv, vecs[i])
				}
			}
			cache.Rows[fresh[k]] = row
		}
	}

	// The mean runs over the reports on disk, in name order, so a deleted report
	// stops counting and the sum does not depend on map order.
	onDisk := make(map[string]bool, len(reports))
	sums := make([]float64, len(bullets))
	n := 0
	for _, f := range reports {
		onDisk[f] = true
		row := cache.Rows[f]
		if len(row) != len(bullets) {
			continue
		}
		n++
		for i, v := range row {
			sums[i] += v
		}
	}
	gone := false
	for f := range cache.Rows {
		if !onDisk[f] {
			delete(cache.Rows, f)
			gone = true
		}
	}
	if len(fresh) > 0 || gone {
		if b, err := json.Marshal(cache); err == nil {
			_ = state.WriteAtomic(t.L.Spike(), b) // a cache that cannot be written is not a run that should fail
		}
		fmt.Printf("  spike background: %d new report(s), %d in the mean\n", len(fresh), n)
	}
	if n < spikeMinOffers {
		return nil
	}
	mean := make([]float64, len(bullets))
	for i, s := range sums {
		mean[i] = s / float64(n)
	}
	return mean
}
