package main

import (
	"strings"
	"testing"
)

func TestPairAccCountsTiesAsHalf(t *testing.T) {
	bullets := []string{"a", "b", "c"}
	want := map[string]bool{"a": true}
	if got := pairAcc(map[string]float64{"a": 2, "b": 1, "c": 3}, bullets, want); got != 0.5 {
		t.Fatalf("one of two pairs right: got %v", got)
	}
	if got := pairAcc(map[string]float64{"a": 1, "b": 1, "c": 0}, bullets, want); got != 0.75 {
		t.Fatalf("one tie, one win: got %v", got)
	}
}

func TestParseJudgeSheet(t *testing.T) {
	bullets := []string{"Built X", "Ran Y", "Cut Z"}
	sheet := strings.Join([]string{
		"## 101 — Acme — Dev (score 4.0) EXEMPLAR",
		"- [ ] a requirement line, not a tick",
		"> - [x] Built X",
		"**CV bullets**",
		"- [x] Built X", "- [ ] Ran Y", "- [X] Cut Z",
		"## 102 — Beta — Dev (score 3.5)",
		"**CV bullets**",
		"- [ ] Built X", "- [x] Ran Y", "- [ ] Cut Z",
	}, "\n")
	cs, err := parseJudgeSheet(sheet, bullets)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || !cs[0].Exemplar || cs[1].Exemplar {
		t.Fatalf("cases: %+v", cs)
	}
	if !cs[0].Want["Built X"] || !cs[0].Want["Cut Z"] || cs[0].Want["Ran Y"] || len(cs[1].Want) != 1 {
		t.Fatalf("ticks: %+v", cs)
	}
	if _, err := parseJudgeSheet(strings.Replace(sheet, "Ran Y", "Ran W", 1), bullets); err == nil {
		t.Fatal("a bullet cv.md no longer has must fail")
	}
}
