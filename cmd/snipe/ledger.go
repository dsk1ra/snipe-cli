package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
)

// cmdLedger builds the fact ledger from cv.md and article-digest.md, merging
// into the one on disk. --check only reads: it says what a build would send to
// the model, so a cv.md edit can be seen before it costs GPU time.
func cmdLedger(args []string) error {
	fs := flag.NewFlagSet("ledger", flag.ExitOnError)
	tidy := fs.Bool("tidy", false, "re-normalise figures and must_keep on the stored ledger; no model call")
	check := fs.Bool("check", false, "list what is stale; do not call the model or write")
	framings := fs.Bool("framings", false, "propose framings from past postings' phrases for bullets not yet framed (needs a built ledger)")
	refresh := fs.Bool("refresh", false, "with --framings: redo bullets already framed, unless a framing on them is decided")
	fk := fs.Int("framing-k", 8, "with --framings: candidate phrases per bullet")
	ffloor := fs.Float64("framing-floor", 0.35, "with --framings: minimum cosine for a candidate (untuned; top-k bounds it)")
	fmin := fs.Int("framing-min-postings", 2, "with --framings: postings a phrase must appear in")
	ollamaURL := fs.String("ollama-url", ollama.DefaultURL, "Ollama base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	l := layout()
	cv, err := os.ReadFile(l.CV())
	if err != nil {
		return err
	}
	digest := embed.ReadFileOr(l.Digest())
	old, err := ledger.Load(l.CVLedger())
	if err != nil {
		return fmt.Errorf("%s: %w", l.CVLedger(), err)
	}

	if *tidy {
		n := ledger.TidyAll(old) + ledger.DropUnsafeFraming(old)
		if err := ledger.Save(l.CVLedger(), old); err != nil {
			return err
		}
		fmt.Printf("tidied %d fact(s)\n", n)
		return nil
	}
	if *framings {
		return cmdFramings(l, old, *check, ledger.FramingOpts{K: *fk, Floor: *ffloor, MinCount: *fmin, Refresh: *refresh}, *ollamaURL)
	}
	if *check {
		s := ledger.Diff(string(cv), digest, old)
		if old.CVHash == ledger.CVHash(string(cv), digest) && s.New == 0 {
			fmt.Printf("current: %d entries, %d bullets\n", s.Entries, s.Bullets)
			return nil
		}
		fmt.Printf("stale: %d new or edited bullet(s) to decompose in %d call(s), %d reused, %d gone from cv.md\n",
			s.New, s.Calls, s.Reused, s.Dropped)
		return nil
	}

	system, err := os.ReadFile(l.Prompt("p3-ledger.md"))
	if err != nil {
		return err
	}
	ctx, cleanup := signalCtx(func() {})
	defer cleanup()
	next, s, err := ledger.Build(ctx, ollama.New(*ollamaURL, ollama.NewTimingLog(l.Timings())),
		string(system), string(cv), digest, old)
	if err != nil {
		return err
	}
	if err := ledger.Save(l.CVLedger(), next); err != nil {
		return err
	}
	fmt.Printf("%d entries, %d bullets, %d facts; %d new, %d reused, %d dropped; %d model call(s)\n",
		s.Entries, s.Bullets, s.Facts, s.New, s.Reused, s.Dropped, s.Calls)
	return nil
}

// cmdFramings is the second pass over an existing ledger. It never rebuilds
// facts, and it saves after the model run even when a later entry failed, since
// every chunk that finished is already merged and costs 30B time to redo.
func cmdFramings(l paths.Layout, old *ledger.Ledger, check bool, o ledger.FramingOpts, ollamaURL string) error {
	if len(old.Entries) == 0 {
		return fmt.Errorf("%s has no entries: run snipe ledger first", l.CVLedger())
	}
	reports, err := ledger.ReadReports(l.Reports())
	if err != nil {
		return err
	}
	corpus := ledger.CollectPhrases(reports, old.Entities, o.MinCount)
	bullets, calls := ledger.FrameDiff(old, o.Refresh)
	if check {
		fmt.Printf("%d phrase(s) in the corpus from %d report(s); %d bullet(s) to frame in up to %d call(s)\n",
			len(corpus), len(reports), bullets, calls)
		return nil
	}
	system, err := os.ReadFile(l.Prompt("p3-framings.md"))
	if err != nil {
		return err
	}
	ol := ollama.New(ollamaURL, ollama.NewTimingLog(l.Timings()))
	ctx, cleanup := signalCtx(func() {})
	defer cleanup()
	ix := &embed.Indexer{OL: ol}
	emb := ledger.CachedEmbed(filepath.Join(l.CacheDir(), "framings-embed.json"), ix.Fingerprint(ctx),
		func(ctx context.Context, texts []string) ([][]float64, error) {
			return embed.Batch(ctx, ol, texts, ollama.ModelEmbed)
		})
	st, runErr := ledger.Frame(ctx, ol, string(system), old, corpus, emb, o, time.Now().Format("2006-01-02"))
	if st.Bullets > 0 {
		if err := ledger.Save(l.CVLedger(), old); err != nil {
			return err
		}
	}
	fmt.Printf("%d phrase(s) in the corpus; %d bullet(s) framed (%d without a candidate); %d model call(s); %d answer(s) dropped\n",
		st.Phrases, st.Bullets, st.NoCandidates, st.Calls, st.Dropped)
	ids := make([]string, 0, len(st.Proposed))
	for id := range st.Proposed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("  %-28s %d framing(s) proposed\n", id, st.Proposed[id])
	}
	return runErr
}
