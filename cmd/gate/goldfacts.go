package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
)

// goldsetFacts scores the same labels with stage 2 retrieving ledger facts
// instead of bullets (PRD §5.2, E5). The labels are on bullets, so a retrieved
// fact counts through its parent bullet, and a bullet that two picks share
// counts once: a slot spent on a second fact of a bullet already shown adds
// nothing for the grader to see. Printed beside the bullet ranker's numbers on
// the same requirements; the verdict is on hit@3 and on distinct ticked bullets
// per slot.
func goldsetFacts(home string, index []embed.Atom, marked []goldCase, vecs [][]float64, bulletHits, bulletSlots int) int {
	l := paths.Layout{Home: home, Code: home}
	led, err := ledger.Load(l.CVLedger())
	if err != nil || len(led.Entries) == 0 {
		fmt.Fprintln(os.Stderr, "no ledger to retrieve facts from:", err)
		return 2
	}
	atoms := phase2.FactAtoms(index, led)
	ix := &embed.Indexer{OL: ollama.New("", nil), CVPath: home + "/batch/cache/cv-fact-index.json"}
	fx, err := ix.LoadCV(context.Background(), atoms)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	parentOf := map[string]int{}
	for i, a := range index {
		parentOf[a.Text] = i
	}
	var demo, catalogue []embed.Atom
	nFacts := 0
	for _, a := range fx {
		if a.Parent != "" {
			nFacts++
		}
		if fit.Catalogue(a.Source) {
			catalogue = append(catalogue, a)
		} else {
			demo = append(demo, a)
		}
	}

	const prodK = 3
	var distinctHits, slots, dup int
	var hit [prodK + 1]int
	for k := 1; k <= prodK; k++ {
		for i, c := range marked {
			picks, err := phase2.TopCandidates(vecs[i], demo, catalogue, k)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			seen := map[int]bool{}
			got := false
			for _, p := range picks {
				key := p.Parent
				if key == "" {
					key = p.Text
				}
				id := parentOf[key]
				if seen[id] {
					if k == prodK {
						dup++
					}
					continue
				}
				seen[id] = true
				if c.want[id] {
					got = true
					if k == prodK {
						distinctHits++
					}
				}
			}
			if got {
				hit[k]++
			}
			if k == prodK {
				slots += len(picks)
			}
		}
	}
	// A third arm: bullets ranked by the best of their own vector and their facts'
	// vectors, so a clause that answers the requirement can pull in its bullet.
	// The pick is still a bullet.
	factsOf := map[string][]embed.Atom{}
	for _, a := range fx {
		if a.Parent != "" {
			factsOf[a.Parent] = append(factsOf[a.Parent], a)
		}
	}
	var maxHit [prodK + 1]int
	var maxDistinct, maxSlots int
	for k := 1; k <= prodK; k++ {
		for i, c := range marked {
			var d []embed.Atom
			for _, a := range index {
				if fit.Catalogue(a.Source) {
					continue
				}
				best, bs := a, cosine(vecs[i], a.Vec)
				for _, f := range factsOf[a.Text] {
					if s := cosine(vecs[i], f.Vec); s > bs {
						best.Vec, bs = f.Vec, s
					}
				}
				d = append(d, best)
			}
			picks, err := phase2.TopCandidates(vecs[i], d, catalogue, k)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			got := false
			for _, p := range picks {
				if c.want[parentOf[p.Text]] {
					got = true
					if k == prodK {
						maxDistinct++
					}
				}
			}
			if got {
				maxHit[k]++
			}
			if k == prodK {
				maxSlots += len(picks)
			}
		}
	}
	n := float64(len(marked))
	fmt.Printf("\nfacts: %d fact atoms from the ledger plus %d other atoms\n", nFacts, len(fx)-nFacts)
	fmt.Printf("  %-22s  hit@1 %.3f  hit@2 %.3f  hit@3 %.3f   p@3 %.3f (distinct ticked bullets per slot)   %d repeat-bullet slots\n\n",
		"facts, two pool", float64(hit[1])/n, float64(hit[2])/n, float64(hit[3])/n, float64(distinctHits)/float64(slots), dup)
	fmt.Printf("  %-22s  hit@1 %.3f  hit@2 %.3f  hit@3 %.3f   p@3 %.3f\n",
		"bullets by best fact", float64(maxHit[1])/n, float64(maxHit[2])/n, float64(maxHit[3])/n, float64(maxDistinct)/float64(maxSlots))
	fmt.Printf("  bullets, two pool: p@3 %.3f\n\n", float64(bulletHits)/float64(bulletSlots))
	return 0
}

func cosine(a, b []float64) float64 {
	c, err := embed.Cosine(a, b)
	if err != nil {
		return 0
	}
	return c
}
