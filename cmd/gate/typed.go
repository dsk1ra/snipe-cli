package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

const typedUsage = "usage: gate typed <home> [--n 30]"

// typedClient is the model client; a test points it at a fake server.
var typedClient = func() *ollama.Client { return ollama.New("", nil) }

// gateTyped is the PRD §5.2 stage 1 check: parse a spread of cached postings
// with the typed schema and report how often the quote holds, what kinds come
// back, and where the parsed terms and the ecosystem/tool patterns name
// different technologies. Parses are cached in batch/cache/typed-stage1.json.
func gateTyped(args []string) int {
	n := 30
	var pos []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--n" && i+1 < len(args) {
			i++
			n = atoi(args[i])
			continue
		}
		pos = append(pos, args[i])
	}
	if len(pos) != 1 || n < 1 {
		fmt.Fprintln(os.Stderr, typedUsage)
		return 2
	}
	home := pos[0]
	code := home
	if b, err := os.ReadFile(filepath.Join(home, "prompts", "p2-jdparse.md")); err != nil || !strings.Contains(string(b), "`terms`") {
		code, _ = os.Getwd()
	}
	l := paths.Layout{Home: home, Code: code}
	files, _ := filepath.Glob(filepath.Join(l.JDs(), "*.txt"))
	sort.Slice(files, func(i, j int) bool { return atoi(filepath.Base(files[i])) < atoi(filepath.Base(files[j])) })
	var picks []string
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && !fit.LooksMultiPosting(string(b)) && strings.TrimSpace(string(b)) != "" {
			picks = append(picks, f)
		}
	}
	if len(picks) > n { // an even spread, not the first n
		step := float64(len(picks)) / float64(n)
		var s []string
		for i := 0; i < n; i++ {
			s = append(s, picks[int(float64(i)*step)])
		}
		picks = s
	}

	cachePath := filepath.Join(l.CacheDir(), "typed-stage1.json")
	cache := map[string]phase2.Parsed{}
	if b, err := os.ReadFile(cachePath); err == nil {
		_ = json.Unmarshal(b, &cache)
	}
	cfg := &config.Config{}
	ev := &phase2.Evaluator{L: l, Cfg: cfg, OL: typedClient(), Model: ollama.ModelEval, NumCtx: 8192, Timeout: 5 * time.Minute}

	var report strings.Builder
	var reqs, typed, withTerms, patternOnly, termsOnly, both, kindsN = 0, 0, 0, 0, 0, 0, map[string]int{}
	fmt.Fprint(&report, "# gate typed: stage 1 parse, typed\n\n")
	for _, f := range picks {
		id := strings.TrimSuffix(filepath.Base(f), ".txt")
		b, _ := os.ReadFile(f)
		jd := strings.TrimSpace(string(b))
		p, ok := cache[id]
		if !ok {
			var err error
			if p, err = ev.ParseJD(context.Background(), atoi(id), jd); err != nil {
				fmt.Fprintf(os.Stderr, "  %s: %v\n", id, err)
				continue
			}
			cache[id] = p
			if out, err := json.MarshalIndent(cache, "", " "); err == nil {
				_ = state.WriteAtomic(cachePath, append(out, '\n'))
			}
			fmt.Fprintf(os.Stderr, "  %s: %d requirements\n", id, len(p.Requirements))
		}
		fmt.Fprintf(&report, "## %s %s\n\n", id, p.Role)
		for _, r := range p.Requirements {
			reqs++
			pat := fit.NamedTech(r.Text)
			if r.Typed {
				typed++
				kindsN[r.Kind]++
				if len(r.Terms) > 0 {
					withTerms++
				}
				switch {
				case len(pat) > 0 && len(r.Terms) > 0:
					both++
				case len(pat) > 0:
					patternOnly++
				case len(r.Terms) > 0:
					termsOnly++
				}
			}
			mark := ""
			if r.Typed && (len(pat) > 0) != (len(r.Terms) > 0) {
				mark = " **differ**"
			}
			fmt.Fprintf(&report, "- [%s] %s%s\n  - terms %v; patterns %v; typed=%t quote=%q\n", r.Kind, r.Text, mark, r.Terms, pat, r.Typed, r.Quote)
		}
		fmt.Fprintln(&report)
	}
	summary := fmt.Sprintf("%d postings, %d requirements, %d typed (%.0f%%); kinds %v\nnames a technology: both %d, patterns only %d, terms only %d (of typed requirements)\n",
		len(picks), reqs, typed, 100*float64(typed)/float64(max(reqs, 1)), kindsN, both, patternOnly, termsOnly)
	out := summary + "\n" + report.String()
	fmt.Print(summary)
	dst := filepath.Join(l.CacheDir(), "typed-stage1.md")
	if err := state.WriteAtomic(dst, []byte(out)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "wrote", dst)
	return 0
}
