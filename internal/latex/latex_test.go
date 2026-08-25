package latex

import "testing"

func TestEscape(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"100% done", `100\% done`},
		{"a & b", `a \& b`},
		{"snake_case", `snake\_case`},
		{"{braces}", `\{braces\}`},
		{`back\slash`, `back\textbackslash{}slash`},
		// The tilde means "approximately" everywhere in this CV, and $\sim$ is
		// what that means. \textasciitilde reads as a macron at this size.
		{"~200 TB", `$\sim$200 TB`},
		{"a < b > c", `a \textless{} b \textgreater{} c`},
		// ATS normalisation runs BEFORE escaping. A pipe introduced after the
		// escaper has run reaches LaTeX bare, where T1 typesets it as an em dash.
		{"CEH • 2025", `CEH \textbar{} 2025`},
		{"a — b", "a - b"},
		{"£50", "GBP 50"},
	} {
		if got := Escape(tc.in); got != tc.want {
			t.Errorf("Escape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEscapeURLLeavesWhatHrefAccepts(t *testing.T) {
	// hyperref reads the target verbatim, so ~ and & must survive untouched —
	// escaping them would break the link.
	got := EscapeURL(`https://x.test/a%b#c\d?e=1&f=~g`)
	want := `https://x.test/a\%b\#c\\d?e=1&f=~g`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBoldMatchesBeforeEscaping(t *testing.T) {
	// The metric regex is calibrated on the source text. Escaping first turns
	// "95%" into "95\%" and "$120k" into "\$120k", and the pattern stops
	// matching — so matching has to come first and the pieces escaped after.
	for _, tc := range []struct{ in, want string }{
		{"Cut latency 95%", `Cut latency \textbf{95\%}`},
		{"$120k budget", `\textbf{\$120k} budget`},
		{"sub-500ms p99", `\textbf{sub-500ms} p99`},
		{"10x load", `\textbf{10x} load`},
		{"50,000 rows", `\textbf{50,000} rows`},
		{"4 weeks", `\textbf{4 weeks}`},
		{"100+ users", `\textbf{100+} users`},
	} {
		if got := Bold(tc.in); got != tc.want {
			t.Errorf("Bold(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBoldLeavesNonMetricsAlone(t *testing.T) {
	// Bare years, version numbers and AES-256 carry no separator, sign or unit,
	// so they are not measurements and bolding them is noise.
	for _, in := range []string{"released 2025", "Version 2.0", "AES-256-GCM"} {
		if got := Bold(in); got != Escape(in) {
			t.Errorf("Bold(%q) = %q, want it left alone", in, got)
		}
	}
}

// The lookbehind RE2 cannot express, applied by index instead.
func TestBoldGuardsTheKMBAlternative(t *testing.T) {
	if got := Bold("2B in B2B"); got != `\textbf{2B} in B2B` {
		t.Errorf("got %q — the 2B inside B2B is not a metric", got)
	}
	if got := Bold("3Mb file"); got != "3Mb file" {
		t.Errorf("got %q — 3Mb is a file size suffix, not a count", got)
	}
	if got := Bold("11K users"); got != `\textbf{11K} users` {
		t.Errorf("got %q — a bare 11K is a metric", got)
	}
}

func TestBoldEmpty(t *testing.T) {
	if got := Bold(""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
