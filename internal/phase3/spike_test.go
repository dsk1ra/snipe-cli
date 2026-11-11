package phase3

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeReport(t *testing.T, dir string, n int, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d-acme-2026-01-01.md", n)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func blockB(n int) string {
	return fmt.Sprintf("| JD Requirement | Candidate evidence | Strength |\n|---|---|---|\n"+
		"| requirement %d about services | e | Strong |\n| a second, longer requirement number %d | e | Gap |\n", n, n*n)
}

// A new report costs one row, and the mean it gives is the one a rebuild from
// nothing gives.
func TestSpikeBackgroundEmbedsOnlyNewReports(t *testing.T) {
	f := &fakeOllama{}
	tl := judgeTailor(t, f.serve(t), "")
	dir := tl.L.Reports()
	os.MkdirAll(dir, 0o755)
	os.MkdirAll(tl.L.CacheDir(), 0o755)
	bullets := []string{b1, b2, b3}
	for n := 1; n <= spikeMinOffers; n++ {
		writeReport(t, dir, n, blockB(n))
	}
	ctx := context.Background()
	bv, err := tl.embed(ctx, bullets)
	if err != nil {
		t.Fatal(err)
	}
	f.embedded = 0

	if tl.spikeBackground(ctx, bullets, bv, "fp") == nil {
		t.Fatal("no background over 20 reports")
	}
	if want := 2 * spikeMinOffers; f.embedded != want {
		t.Fatalf("first build embedded %d texts, want %d", f.embedded, want)
	}
	tl.spikeBackground(ctx, bullets, bv, "fp")
	if want := 2 * spikeMinOffers; f.embedded != want {
		t.Fatalf("an unchanged corpus embedded again: %d texts", f.embedded)
	}

	writeReport(t, dir, 21, blockB(21))
	writeReport(t, dir, 22, "# a report with no evidence table\n")
	before := f.embedded
	inc := tl.spikeBackground(ctx, bullets, bv, "fp")
	if got := f.embedded - before; got != 2 {
		t.Fatalf("one new report embedded %d texts, want its 2 requirements", got)
	}

	os.Remove(tl.L.Spike())
	full := tl.spikeBackground(ctx, bullets, bv, "fp")
	for i := range full {
		if inc[i] != full[i] {
			t.Fatalf("bullet %d: incremental mean %v, rebuilt mean %v", i, inc[i], full[i])
		}
	}

	// A different embedder is a different vector space: nothing carries over.
	before = f.embedded
	tl.spikeBackground(ctx, bullets, bv, "other")
	if got := f.embedded - before; got != 2*(spikeMinOffers+1) {
		t.Fatalf("a new fingerprint embedded %d texts, want everything", got)
	}
}
