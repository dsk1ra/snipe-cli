package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/extract"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

const fitxUsage = "usage: gate fitx <home> [--model snipe-screen] [--limit N] [--only location|language|seniority|stack]"

// fitxClient is the model client; a test points it at a fake server.
var fitxClient = func() *ollama.Client { return ollama.New("", nil) }

// fitxCaps are the three caps, in report order.
var fitxCaps = []string{"location", "language", "seniority", "stack"}

// fitxPair is one cap decided twice: by the regex path and from the facts.
type fitxPair struct {
	old, new       fit.Caps
	oldWhy, newWhy string
}

func (p fitxPair) agree() bool { return p.old.CV == p.new.CV && p.old.NS == p.new.NS }

func (p fitxPair) capped(old bool) bool {
	if old {
		return p.old.CV < 5 || p.old.NS < 5
	}
	return p.new.CV < 5 || p.new.NS < 5
}

// fitxPosting is everything the report needs about one posting.
type fitxPosting struct {
	id, title string
	raw, post string // the file, and the cleaned text the model read
	facts     extract.Facts
	caps      map[string]fitxPair
}

// gateFitx is the PRD §5.1 shadow: the three word-list caps computed twice over
// the cached postings, once by regex and once from quoted facts, with every
// disagreement printed for a human to read. Nothing here changes a score.
func gateFitx(args []string) int {
	model, limit, only := ollama.ModelScreen, 0, ""
	var pos []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model", "--limit", "--only":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, fitxUsage)
				return 2
			}
			i++
			switch args[i-1] {
			case "--model":
				model = args[i]
			case "--limit":
				limit = atoi(args[i])
			case "--only":
				only = args[i]
			}
		default:
			pos = append(pos, args[i])
		}
	}
	if len(pos) != 1 || (only != "" && !contains(fitxCaps, only)) {
		fmt.Fprintln(os.Stderr, fitxUsage)
		return 2
	}
	home := pos[0]
	// The prompt belongs to the code and the data to the home; they are the same
	// directory in a checkout, but a deployed home can be older than the code.
	code := home
	if _, err := os.Stat(filepath.Join(home, "prompts", "p1-cvstack.md")); err != nil {
		code, _ = os.Getwd()
	}
	l := paths.Layout{Home: home, Code: code}
	cv, err1 := os.ReadFile(l.CV())
	prof, err2 := os.ReadFile(l.ProfileYML())
	system, err3 := config.Prompt(l, "p1-extract.md")
	cvSystem, err4 := config.Prompt(l, "p1-cvstack.md")
	for _, err := range []error{err1, err2, err3, err4} {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	ex := extract.New(fitxClient(), l.CacheDir(), model, system)
	ex.CVSystem = cvSystem
	if led, err := ledger.Load(l.CVLedger()); err == nil {
		ex.Entities = led.EntityNames()
	}
	cvLangs, err := ex.CVLanguages(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "no stack comparison:", err)
	} else {
		fmt.Fprintf(os.Stderr, "CV languages: %s\n", strings.Join(cvLangs, ", "))
	}

	files, _ := filepath.Glob(filepath.Join(l.JDs(), "*.txt"))
	sort.Slice(files, func(i, j int) bool {
		a, b := atoi(strings.TrimSuffix(filepath.Base(files[i]), ".txt")), atoi(strings.TrimSuffix(filepath.Base(files[j]), ".txt"))
		if a != b {
			return a < b
		}
		return files[i] < files[j]
	})

	cache := state.NewCache()
	var rows []fitxPosting
	var multi, failed int
	for _, f := range files {
		if limit > 0 && len(rows) >= limit {
			break
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		raw := string(b)
		if fit.LooksMultiPosting(raw) {
			multi++
			continue
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		post := extract.Posting(raw)
		id := strings.TrimSuffix(filepath.Base(f), ".txt")
		title := ""
		if n := atoi(id); n > 0 {
			if a := cache.Score(l.Scores(), n); a != nil {
				title = a.Role
			}
		}
		facts, err := ex.Extract(context.Background(), post)
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", id, err)
			continue
		}
		p := fitxPosting{id: id, title: title, raw: raw, post: post, facts: facts, caps: map[string]fitxPair{}}
		var pr fitxPair
		pr.old, pr.oldWhy = fit.LocationMismatchCap(raw, string(prof))
		pr.new, pr.newWhy = fit.LocationCapFrom(facts.Attendance, string(prof))
		p.caps["location"] = pr
		pr = fitxPair{}
		pr.old, pr.oldWhy = fit.LanguageMismatchCap(raw, string(cv))
		pr.new, pr.newWhy = fit.LanguageCapFrom(facts.Languages, string(cv))
		p.caps["language"] = pr
		pr = fitxPair{}
		pr.old = fit.SeniorityCaps(title, raw, 0, "")
		pr.new = fit.SeniorityCaps(title, "", facts.Years.MinYears, "") // title words from the regexes, years from the extraction
		pr.oldWhy, pr.newWhy = pr.old.Reason, pr.new.Reason
		p.caps["seniority"] = pr
		pr = fitxPair{}
		sm := fit.StackMismatchCap(raw, string(cv), 0, 0)
		xm := fit.StackCapFrom(facts.Stack, cvLangs)
		pr.old, pr.oldWhy = fit.Caps{CV: sm.Cap, NS: 5}, strings.Join(sm.Missing, "+")
		pr.new, pr.newWhy = fit.Caps{CV: xm.Cap, NS: 5}, strings.Join(xm.Missing, "+")
		if err != nil { // no CV languages: nothing to compare
			pr.old, pr.new = fit.Caps{CV: 5, NS: 5}, fit.Caps{CV: 5, NS: 5}
		}
		p.caps["stack"] = pr
		rows = append(rows, p)
	}
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "no postings extracted from %s (%d multi-posting, %d failed)\n", l.JDs(), multi, failed)
		return 2
	}

	var buf bytes.Buffer
	fitxReport(&buf, model, only, rows, multi, failed, ex.Calls)
	os.Stdout.Write(buf.Bytes())
	out := filepath.Join(l.CacheDir(), "fitx-disagreements.md")
	if err := state.WriteAtomic(out, buf.Bytes()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", out)
	return 0
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func fitxReport(w io.Writer, model, only string, rows []fitxPosting, multi, failed, calls int) {
	fmt.Fprintf(w, "# gate fitx: regex caps against caps from quoted facts\n\n")
	fmt.Fprintf(w, "model %s; %d postings extracted (%d model calls this run), %d multi-posting skipped, %d extraction failures\n\n", model, len(rows), calls, multi, failed)

	which := fitxCaps
	if only != "" {
		which = []string{only}
	}

	fmt.Fprintln(w, "## Agreement")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| cap | agree | both none | both capped | regex only | facts only | both capped, different ceiling |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
	for _, c := range which {
		var none, both, oldOnly, newOnly, diff int
		for _, r := range rows {
			p := r.caps[c]
			switch {
			case !p.capped(true) && !p.capped(false):
				none++
			case p.capped(true) && p.capped(false) && p.agree():
				both++
			case p.capped(true) && p.capped(false):
				diff++
			case p.capped(true):
				oldOnly++
			default:
				newOnly++
			}
		}
		fmt.Fprintf(w, "| %s | %d/%d | %d | %d | %d | %d | %d |\n", c, none+both, len(rows), none, both, oldOnly, newOnly, diff)
	}

	for _, c := range which {
		fmt.Fprintf(w, "\n## Disagreements: %s\n", c)
		n := 0
		for _, r := range rows {
			p := r.caps[c]
			if p.agree() {
				continue
			}
			n++
			fitxDisagreement(w, c, r, p)
		}
		if n == 0 {
			fmt.Fprintln(w, "\nnone")
		}
	}

	fmt.Fprintf(w, "\n## Extraction stats\n\n")
	fitxStats(w, rows)
}

func fitxDisagreement(w io.Writer, cap string, r fitxPosting, p fitxPair) {
	fmt.Fprintf(w, "\n### %s (%s)\n\n", r.id, cap)
	fmt.Fprintf(w, "- regex: %s\n- facts: %s\n", fitxDecision(p.old, p.oldWhy), fitxDecision(p.new, p.newWhy))
	f := r.facts
	switch cap {
	case "location":
		a := f.Attendance
		fmt.Fprintf(w, "- attendance: mode=%s days_per_week=%d days_per_month=%d place=%q quote=%q (claimed=%t quote_ok=%t)\n",
			a.Mode, a.DaysPerWeek, a.DaysPerMonth, a.Place, a.Quote, a.Claimed, a.QuoteOK)
		if a.QuoteOK {
			fmt.Fprintf(w, "- facts window: %s\n", quoteWindow(r.post, a.Quote))
		}
		if p.oldWhy != "" {
			fmt.Fprintf(w, "- regex window: %s\n", termWindow(r.raw, `\b`+regexp.QuoteMeta(p.oldWhy)+`\b`))
		}
	case "language":
		fmt.Fprintf(w, "- languages: %d claimed, %d kept\n", f.LanguagesClaimed, f.LanguagesOK)
		for _, x := range f.Languages {
			fmt.Fprintf(w, "  - %s (%s) quote=%q\n    window: %s\n", x.Language, x.Requirement, x.Quote, quoteWindow(r.post, x.Quote))
		}
		if p.oldWhy != "" {
			fmt.Fprintf(w, "- regex window: %s\n", termWindow(r.raw, `\b`+regexp.QuoteMeta(p.oldWhy)+`\b`))
		}
	case "stack":
		for _, x := range f.Stack {
			fmt.Fprintf(w, "- stack: %s (%s) quote=%q\n", x.Language, x.Requirement, x.Quote)
		}
		if p.oldWhy != "" {
			fmt.Fprintf(w, "- regex found only: %s\n", p.oldWhy)
		}
	case "seniority":
		fmt.Fprintf(w, "- title used by the regex path (cached score): %q\n", r.title)
		y := f.Years
		fmt.Fprintf(w, "- years: min_years=%d quote=%q (claimed=%t quote_ok=%t)\n", y.MinYears, y.Quote, y.Claimed, y.QuoteOK)
		if y.QuoteOK {
			fmt.Fprintf(w, "- facts window: %s\n", quoteWindow(r.post, y.Quote))
		}
		// A reading aid only; the patterns the cap used are in internal/fit.
		fmt.Fprintf(w, "- regex window (first \"N years\"): %s\n", termWindow(r.raw, `\d+\+?\s*years`))
	}
}

func fitxDecision(c fit.Caps, why string) string {
	if c.CV >= 5 && c.NS >= 5 {
		return "no cap"
	}
	return fmt.Sprintf("cap %d/%d %s", c.CV, c.NS, why)
}

// termWindow is about 200 characters of s around the first match of pattern, or
// a note that there is none. Case-insensitive, whitespace collapsed.
func termWindow(s, pattern string) string {
	loc := regexp.MustCompile(`(?i)` + pattern).FindStringIndex(s)
	if loc == nil {
		return "(no match in the posting)"
	}
	return around(s, loc[0], loc[1])
}

// quoteWindow is the same around a quote, located in the normalised posting.
func quoteWindow(post, quote string) string {
	n := extract.Norm(post)
	q := extract.Norm(quote)
	i := strings.Index(n, q)
	if i < 0 {
		return "(quote not found)"
	}
	return around(n, i, i+len(q))
}

func around(s string, lo, hi int) string {
	const pad = 100
	a, b := max(lo-pad, 0), min(hi+pad, len(s))
	for a < len(s) && !utf8.RuneStart(s[a]) {
		a++
	}
	for b < len(s) && !utf8.RuneStart(s[b]) {
		b++
	}
	return strings.Join(strings.Fields(s[a:lo]), " ") + " [[" + strings.Join(strings.Fields(s[lo:hi]), " ") + "]] " + strings.Join(strings.Fields(s[hi:b]), " ")
}

func fitxStats(w io.Writer, rows []fitxPosting) {
	n := len(rows)
	var att, attOK, attUnk, yrs, yrsOK, yrsUnk, lc, lk, withLang, withStack int
	for _, r := range rows {
		f := r.facts
		if f.Attendance.Claimed {
			att++
		}
		if f.Attendance.QuoteOK {
			attOK++
		}
		if f.Attendance.Mode == extract.Unspecified && f.Attendance.DaysPerWeek == 0 && f.Attendance.DaysPerMonth == 0 {
			attUnk++
		}
		if f.Years.Claimed {
			yrs++
		}
		if f.Years.QuoteOK {
			yrsOK++
		}
		if f.Years.MinYears == 0 {
			yrsUnk++
		}
		if len(f.Stack) > 0 {
			withStack++
		}
		lc += f.LanguagesClaimed
		lk += f.LanguagesOK
		if len(f.Languages) > 0 {
			withLang++
		}
	}
	pct := func(a, b int) string {
		if b == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.0f%%", 100*float64(a)/float64(b))
	}
	fmt.Fprintln(w, "| fact | claimed by the model | quote_ok | quote_ok rate | unknown after checking |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	fmt.Fprintf(w, "| attendance | %d | %d | %s | %d (%s) |\n", att, attOK, pct(attOK, att), attUnk, pct(attUnk, n))
	fmt.Fprintf(w, "| language (per entry) | %d | %d | %s | %d postings name one (%s) |\n", lc, lk, pct(lk, lc), withLang, pct(withLang, n))
	fmt.Fprintf(w, "| years | %d | %d | %s | %d (%s) |\n", yrs, yrsOK, pct(yrsOK, yrs), yrsUnk, pct(yrsUnk, n))
	fmt.Fprintf(w, "| stack (postings naming a language) | | | | %d (%s) |\n", withStack, pct(withStack, n))
	fmt.Fprintln(w, "\nUnknown is the share of postings where the fact ended up empty, whether the model said so or the quote failed.")
}
