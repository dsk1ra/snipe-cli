package main

import (
	"math"
	"reflect"
	"testing"
)

// What pdftotext -layout prints for the template: headings flush left, entries
// one space in, bullets deeper, a blank line after each bullet's first line.
const fixturePage = `                                         Ada Lovelace
                                                Software Engineer

Technical Skills
 Languages: Java, Go, Python

Work Experience
 Napier University                                                                       Sep 2025 - Present
 Teaching Assistant                                                                   Edinburgh, United Kingdom
    • Teach and mentor 800+ undergraduates in Java, C++ and Python; rewrote the setup guide the

      cohort uses, cutting setup from 2 hours to 30 minutes
 UBWIS                                                                                           Oct 2024 - Sep 2025
 Software Engineer                                                               Edinburgh, United Kingdom (remote)
    • Built a live membership platform with Stripe subscriptions

    • Ran it on AWS from Terraform and GitHub Actions, instrumented with OpenTelemetry into Grafana,
      set the SLOs the team ran on
 Acme                                                                                            Jan 2022 - Sep 2024
 Developer                                                                       London, UK
    • Wrote Kafka consumers

    • Cut p99 latency of the order saga by 40%

Projects
 Re:Link, peer-to-peer remote access                                                 Dissertation
   • P2P remote desktop in Rust with an AES-256-GCM channel, fuzzed

     and externally reviewed
     Rust, Tokio, QUIC | dsk1ra/re-link

Education
 BEng Software Engineering                                                               Sep 2022 - Jul 2026
    • Awards: 2nd of 24, CTF
`

func TestMeasure(t *testing.T) {
	m := measure(fixturePage, terms([]string{"Kafka consumers in production", "Stripe billing"}))
	if m.Pages != 1 {
		t.Errorf("pages = %d", m.Pages)
	}
	if m.Lines != 26 {
		t.Errorf("lines = %d", m.Lines)
	}
	// Napier (1 bullet) and UBWIS (2) and Acme (2): one starved. Education is
	// not Experience, so its single bullet does not count.
	if m.ExpStarved != 1 {
		t.Errorf("exp_starved = %d", m.ExpStarved)
	}
	// 5 experience bullets + 1 project bullet. "Stripe" and "Kafka" lead two.
	if m.Bullets != 6 {
		t.Fatalf("bullets = %d", m.Bullets)
	}
	if want := 2.0 / 6; math.Abs(m.FrontLoad-want) > 1e-9 {
		t.Errorf("front_load = %v, want %v", m.FrontLoad, want)
	}
}

func TestMeasureTwoPages(t *testing.T) {
	m := measure(fixturePage+"\f"+" Overflow\n", nil)
	if m.Pages != 2 || b2f(m.Pages <= 1) != 0 {
		t.Errorf("pages = %d", m.Pages)
	}
	if m.FrontLoad != 0 {
		t.Errorf("no terms must score 0, got %v", m.FrontLoad)
	}
}

func TestHasTermOnlyLooksAtTheFront(t *testing.T) {
	ts := terms([]string{"Terraform"})
	if !hasTerm("Ran it on AWS from Terraform and more", ts) {
		t.Error("term in the first ten words")
	}
	if hasTerm("one two three four five six seven eight nine ten Terraform", ts) {
		t.Error("term past the first ten words")
	}
}

func TestEntriesWrappedBullet(t *testing.T) {
	got := entries([]string{" Co", " Dev", "    • first line", "", "      second line", "    • next"})
	want := [][]string{{"first line second line", "next"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

func TestParseOfferIDs(t *testing.T) {
	got := parseOfferIDs([]string{
		"012-acme-2026-07-18.md", "2026-07-30_keyrock_001", "# skip", "", "7", "012", "notes.txt", "044  # 4.3 Acme",
	})
	if want := []string{"001", "007", "012", "044"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestAAFloor(t *testing.T) {
	f := aaFloor([]float64{1, 2, 3}, []float64{1, 4, 3})
	if math.Abs(f.Mean-2.0/3) > 1e-9 || f.Max != 2 {
		t.Errorf("floor = %+v", f)
	}
	if (aaFloor(nil, nil) != floor{}) {
		t.Error("empty sample")
	}
}

func TestSuiteShape(t *testing.T) {
	// The §8.1 table is final; the two judge rows stay in it as n/a.
	var na []string
	for _, m := range suite {
		if m.na != "" {
			na = append(na, m.name)
		}
	}
	if want := []string{"must_have_evidence", "pairwise_pref"}; !reflect.DeepEqual(na, want) || len(suite) != 15 {
		t.Errorf("suite has %d rows, n/a %v", len(suite), na)
	}
}
