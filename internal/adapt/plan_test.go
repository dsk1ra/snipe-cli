package adapt

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
)

const (
	saga    = "Modelled checkout as a 6-step saga so a failed order reverses cleanly, with row locks against concurrent updates"
	svcs    = "Built retail operations as 10 Java and Spring Boot services, each owning its own database and talking to the others only through a Kafka log"
	deploy  = "Ran the platform on AWS from Terraform and GitHub Actions, shipping every merge to production"
	billing = "Reconciled supplier invoices against the finance ledger every night"
)

func bullet(text string, facts []ledger.Fact, fr ...ledger.Framing) ledger.Bullet {
	return ledger.Bullet{Hash: ledger.Hash(text), Text: text, Facts: facts, Framings: fr}
}

// fixture is two entries: a project whose second bullet carries framings, and a
// job whose entry holds inferences in each state.
func fixture(inferred ...ledger.Fact) *ledger.Ledger {
	ops := ledger.Entry{ID: "ops", Kind: "experience", Title: "Engineer - Acme", Bullets: []ledger.Bullet{
		bullet(deploy, []ledger.Fact{
			{ID: "ops/1a", Text: "Ran the platform on AWS", Entities: []string{"AWS"}},
			{ID: "ops/1b", Text: "Deployed from Terraform and GitHub Actions", Entities: []string{"Terraform", "GitHub Actions"}},
		}),
		bullet(billing, []ledger.Fact{{ID: "ops/2a", Text: "Reconciled supplier invoices nightly"}}),
	}}
	if len(inferred) > 0 {
		ops.Bullets = append(ops.Bullets, ledger.Bullet{Hash: ledger.InferredHash, Facts: inferred})
	}
	return &ledger.Ledger{
		Entries: []ledger.Entry{
			{ID: "shop", Kind: "project", Title: "Store Platform", Qualifiers: []string{"Supervised Project"}, Bullets: []ledger.Bullet{
				bullet(saga, []ledger.Fact{
					{ID: "shop/1a", Text: "Modelled checkout as a 6-step saga", Figures: []string{"6-step"}, MustKeep: true},
					{ID: "shop/1b", Text: "A failed order reverses cleanly"},
					{ID: "shop/1c", Text: "Used row locks against concurrent updates", Entities: []string{"row locks"}},
				}),
				bullet(svcs, []ledger.Fact{
					{ID: "shop/2a", Text: "Built retail operations as 10 Java and Spring Boot services", Entities: []string{"Java", "Spring Boot"}, Figures: []string{"10"}, MustKeep: true},
					{ID: "shop/2b", Text: "Each service owns its own database"},
					{ID: "shop/2c", Text: "Services talk to each other only through a Kafka log", Entities: []string{"Kafka"}},
				},
					ledger.Framing{Phrase: "event-driven microservices", From: []string{"shop/2a", "shop/2c"}, Approved: true},
					ledger.Framing{Phrase: "database-per-service", From: []string{"shop/2b"}},
					ledger.Framing{Phrase: "CQRS", From: []string{"shop/2b"}, Rejected: true},
				),
			}},
			ops,
		},
		Entities: map[string][]string{"PostgreSQL": {"PostgreSQL", "Postgres"}},
	}
}

func inferred(term, status string) ledger.Fact {
	return ledger.Fact{ID: "ops/9" + term[:1], Text: "Used " + term, Entities: []string{term}, Source: "inferred", Status: status}
}

// ev is the evidence stage 2 prints for a bullet: the entry header, then the text.
func ev(header, text string) string { return header + ": " + text }

func row(req string, must bool, strength, evidence string) phase2.Row {
	return phase2.Row{Requirement: req, MustHave: must, Strength: strength, Evidence: evidence}
}

func TestTargetsMustHavesFirst(t *testing.T) {
	l := fixture()
	p := Posting{Rows: []phase2.Row{
		row("nice Strong", false, fit.Strong, ev("Store Platform", svcs)),
		row("must Transferable", true, fit.Transferable, ev("Store Platform", svcs)),
		row("must Gap", true, fit.Gap, "—"),
		row("must on another line", true, fit.Strong, ev("Store Platform", saga)),
		row("must Strong", true, fit.Strong, ev("Store Platform", svcs)),
		row("nice Transferable", false, fit.Transferable, ev("Store Platform", svcs)),
	}}
	var got []string
	for _, tg := range BuildPlan(l, svcs, p).Targets {
		got = append(got, tg.Requirement)
	}
	want := []string{"must Strong", "must Transferable", "nice Strong", "nice Transferable"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("targets = %v, want %v", got, want)
	}
}

// The report clips evidence at 160 characters and swaps pipes for slashes; the
// plan has to find the bullet from that.
func TestTargetsFromReportEvidence(t *testing.T) {
	l := fixture()
	clipped := ev("Software Engineer", svcs)[:100]
	p := Posting{Rows: []phase2.Row{row("Kafka services", true, fit.Strong, clipped)}}
	if got := BuildPlan(l, svcs, p).Targets; len(got) != 1 {
		t.Errorf("clipped evidence did not locate its bullet: %v", got)
	}
}

func TestLead(t *testing.T) {
	l := fixture()
	for name, tc := range map[string]struct{ req, want string }{
		"fact further down the line": {"services talk through a Kafka log", "shop/2c"},
		"source already opens":       {"Java and Spring Boot services for retail operations", ""},
		"nothing in common":          {"stakeholder management", ""},
	} {
		p := Posting{Rows: []phase2.Row{row(tc.req, true, fit.Strong, ev("Store Platform", svcs))}}
		if got := BuildPlan(l, svcs, p).Lead; got != tc.want {
			t.Errorf("%s: lead = %q, want %q", name, got, tc.want)
		}
	}
	if got := BuildPlan(l, svcs, Posting{}).Lead; got != "" {
		t.Errorf("no targets: lead = %q, want empty", got)
	}
}

// A closer fact from the injected scorer wins over word overlap.
func TestLeadUsesInjectedSim(t *testing.T) {
	l := fixture()
	p := Posting{
		Rows: []phase2.Row{row("resilience", true, fit.Strong, ev("Store Platform", svcs))},
		Sim: func(_, fact string) float64 {
			if strings.Contains(fact, "own database") {
				return 0.9
			}
			return 0.1
		},
	}
	if got := BuildPlan(l, svcs, p).Lead; got != "shop/2b" {
		t.Errorf("lead = %q, want shop/2b", got)
	}
}

func TestAlignmentFallbackNeedsSimAndCutoff(t *testing.T) {
	l := fixture()
	rows := []phase2.Row{
		row("sagas", true, fit.Strong, ev("Store Platform", saga)),
		row("a gap", true, fit.Gap, "—"),
	}
	sim := func(_, fact string) float64 {
		if strings.Contains(fact, "Kafka") {
			return 0.8
		}
		return 0.2
	}
	if got := BuildPlan(l, svcs, Posting{Rows: rows, Sim: sim}).Targets; len(got) != 0 {
		t.Errorf("without a cut-off the bullet should have no targets, got %v", got)
	}
	got := BuildPlan(l, svcs, Posting{Rows: rows, Sim: sim, AlignMin: 0.5}).Targets
	if len(got) != 1 || got[0].Requirement != "sagas" {
		t.Errorf("fallback targets = %v, want the one non-Gap row and never the Gap", got)
	}
}

func terms(kind Kind, texts ...string) []Term {
	var out []Term
	for _, s := range texts {
		out = append(out, Term{Text: s, Kind: kind})
	}
	return out
}

func TestAllowedAndForbidden(t *testing.T) {
	l := fixture(inferred("Helm", "approved"), inferred("Nomad", "rejected"), inferred("Pulumi", ""))
	l.Entries[0].Bullets[1].Facts[0].Entities = append(l.Entries[0].Bullets[1].Facts[0].Entities, "PostgreSQL")
	posting := Posting{
		Rows: []phase2.Row{row("We run event-driven microservices", true, fit.Strong, ev("Store Platform", svcs))},
		Terms: terms(KindUntyped, "Kafka", "Java", "Postgres", "Spark", "event-driven microservices",
			"database-per-service", "CQRS", "Helm", "Nomad", "Pulumi", "Terraform", "kafka"),
	}
	for name, tc := range map[string]struct {
		bullet           string
		allowed, forbids []string
	}{
		"entities, alias and an approved framing": {svcs,
			[]string{"Kafka", "Java", "Postgres", "event-driven microservices"},
			[]string{"Spark", "database-per-service", "CQRS", "Helm", "Nomad", "Pulumi", "Terraform"}},
		"approved inference reaches every line of its entry": {billing,
			[]string{"Helm"},
			[]string{"Kafka", "Java", "Postgres", "Spark", "event-driven microservices", "database-per-service", "CQRS", "Nomad", "Pulumi", "Terraform"}},
		"entity of another entry stays out": {saga,
			nil,
			[]string{"Kafka", "Java", "Postgres", "Spark", "event-driven microservices", "database-per-service", "CQRS", "Helm", "Nomad", "Pulumi", "Terraform"}},
	} {
		got := BuildPlan(l, tc.bullet, posting)
		if !reflect.DeepEqual(got.AllowedTerms, tc.allowed) {
			t.Errorf("%s: allowed = %v, want %v", name, got.AllowedTerms, tc.allowed)
		}
		if !reflect.DeepEqual(got.Forbidden, tc.forbids) {
			t.Errorf("%s: forbidden = %v, want %v", name, got.Forbidden, tc.forbids)
		}
	}
}

// A framing the posting asks for in a sentence rather than a keyword is still
// the phrase the line may use.
func TestApprovedFramingFromRequirementText(t *testing.T) {
	p := Posting{Rows: []phase2.Row{row("Experience designing event-driven microservices", true, fit.Strong, ev("x", svcs))}}
	if got := BuildPlan(fixture(), svcs, p).AllowedTerms; !reflect.DeepEqual(got, []string{"event-driven microservices"}) {
		t.Errorf("allowed = %v", got)
	}
}

func TestTermsIsTheUnion(t *testing.T) {
	pl := Plan{AllowedTerms: []string{"Kafka"}, Proposed: []Proposal{{Term: "Helm"}}, Forbidden: []string{"Helm", "Spark"}}
	if got, want := pl.Terms(), []string{"Kafka", "Helm", "Spark"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Terms = %v, want %v", got, want)
	}
}

// Each of the three conditions of PRD §1.1 must be able to refuse alone.
func TestProposalNeedsAllThree(t *testing.T) {
	base := func() Posting {
		return Posting{
			Score: 4.3,
			Rows:  []phase2.Row{row("Kubernetes deployments", true, fit.Transferable, ev("Engineer", deploy))},
			Terms: terms(KindTool, "Kubernetes"),
		}
	}
	for name, tc := range map[string]struct {
		mod  func(*Posting)
		want bool
	}{
		"all three hold":             {func(*Posting) {}, true},
		"exactly at the bar":         {func(p *Posting) { p.Score = 4.0 }, true},
		"capability counts":          {func(p *Posting) { p.Terms[0].Kind = KindCapability }, true},
		"profile raises the bar":     {func(p *Posting) { p.InferMinScore = 4.5 }, false},
		"score 3.9":                  {func(p *Posting) { p.Score = 3.9 }, false},
		"domain term":                {func(p *Posting) { p.Terms[0].Kind = KindDomain }, false},
		"credential term":            {func(p *Posting) { p.Terms[0].Kind = KindCredential }, false},
		"eligibility term":           {func(p *Posting) { p.Terms[0].Kind = KindEligibility }, false},
		"untyped keyword":            {func(p *Posting) { p.Terms[0].Kind = KindUntyped }, false},
		"graded Strong, not same":    {func(p *Posting) { p.Rows[0].Strength = fit.Strong }, false},
		"graded Gap":                 {func(p *Posting) { p.Rows[0].Strength = fit.Gap }, false},
		"Transferable on other line": {func(p *Posting) { p.Rows[0].Evidence = ev("Engineer", billing) }, false},
		"no evidence at all":         {func(p *Posting) { p.Rows = nil }, false},
	} {
		p := base()
		tc.mod(&p)
		got := BuildPlan(fixture(), deploy, p)
		if has := len(got.Proposed) == 1; has != tc.want {
			t.Errorf("%s: proposed = %v, want proposed=%v", name, got.Proposed, tc.want)
		}
		// Proposed or not, it is no claim yet.
		if !reflect.DeepEqual(got.Forbidden, []string{"Kubernetes"}) || len(got.AllowedTerms) != 0 {
			t.Errorf("%s: allowed %v forbidden %v", name, got.AllowedTerms, got.Forbidden)
		}
	}
}

func TestRejectedInferenceNeverProposedAgain(t *testing.T) {
	p := Posting{
		Score: 4.8,
		Rows:  []phase2.Row{row("Kubernetes", true, fit.Transferable, ev("Engineer", deploy))},
		Terms: terms(KindTool, "Kubernetes"),
	}
	for status, want := range map[string]int{"rejected": 0, "": 1} {
		got := BuildPlan(fixture(inferred("Kubernetes", status)), deploy, p)
		if len(got.Proposed) != want {
			t.Errorf("status %q: proposed = %v, want %d", status, got.Proposed, want)
		}
	}
	// And an approval turns it into an allowed term instead.
	got := BuildPlan(fixture(inferred("Kubernetes", "approved")), deploy, p)
	if len(got.Proposed) != 0 || !reflect.DeepEqual(got.AllowedTerms, []string{"Kubernetes"}) {
		t.Errorf("approved: proposed %v allowed %v", got.Proposed, got.AllowedTerms)
	}
}

func TestStaleBulletIsVerbatim(t *testing.T) {
	text := "A bullet edited since the ledger was built, so its hash is unknown to it"
	got := BuildPlan(fixture(), text, Posting{Score: 5, Terms: terms(KindTool, "Kafka")})
	if !got.Verbatim || got.EntryID != "" || got.Bullet.Text != text {
		t.Errorf("plan = %+v, want a verbatim plan carrying the text", got)
	}
	if got.MaxLines != cvselect.BulletLines(text) {
		t.Errorf("MaxLines = %d", got.MaxLines)
	}
	if len(got.Targets)+len(got.AllowedTerms)+len(got.Forbidden)+len(got.Proposed) != 0 || got.Lead != "" {
		t.Errorf("a stale bullet carries nothing but its line count: %+v", got)
	}
	if BuildPlan(nil, text, Posting{}).Verbatim != true {
		t.Error("nil ledger should be verbatim")
	}
}

func TestMustKeepAndMaxLines(t *testing.T) {
	got := BuildPlan(fixture(), svcs, Posting{})
	want := MustKeep{Facts: []string{"shop/2a"}, Figures: []string{"10"}, Qualifiers: []string{"Supervised Project"}}
	if !reflect.DeepEqual(got.MustKeep, want) {
		t.Errorf("must keep = %+v, want %+v", got.MustKeep, want)
	}
	if got.MaxLines != cvselect.BulletLines(svcs) || got.EntryID != "shop" || got.Bullet.Hash != ledger.Hash(svcs) {
		t.Errorf("plan = %+v", got)
	}
}

func TestCandidates(t *testing.T) {
	l := fixture(inferred("Pulumi", ""))
	p := Posting{
		ID: "345", Date: "2026-10-21", Score: 4.3,
		Rows: []phase2.Row{
			row("Kubernetes and Pulumi", true, fit.Transferable, ev("Engineer", deploy)),
			row("Kubernetes at nightly scale", false, fit.Transferable, ev("Engineer", billing)),
		},
		Terms: terms(KindTool, "Kubernetes", "Pulumi"),
	}
	plans := PlanAll(l, []string{deploy, billing, "stale"}, p)
	got := Candidates(l, plans, p)
	// Kubernetes is proposed on two lines of one entry but queued once, and
	// Pulumi is already waiting in the ledger.
	if len(got) != 1 {
		t.Fatalf("candidates = %+v, want one", got)
	}
	c := got[0]
	f := c.Fact
	if c.EntryID != "ops" || f.Text != "Used Kubernetes" || f.Source != "inferred" || f.Status != "" ||
		f.Approved != "345@2026-10-21" || !reflect.DeepEqual(f.Entities, []string{"Kubernetes"}) || c.Why == "" {
		t.Errorf("candidate = %+v", c)
	}
	if len(l.Entries[1].Bullets) != 3 {
		t.Error("Candidates must not touch the ledger")
	}
}

func TestContains(t *testing.T) {
	for _, tc := range []struct {
		s, term string
		want    bool
	}{
		{"talk through a Kafka log", "kafka", true},
		{"Kafkaesque", "Kafka", false},
		{"go through the list", "Go", false},
		{"written in Go and Rust", "Go", true},
		{"C++ and C#", "C++", true},
		{"Node.js services", "Node.js", true},
		{"", "x", false},
	} {
		if got := contains(tc.s, tc.term); got != tc.want {
			t.Errorf("contains(%q, %q) = %v", tc.s, tc.term, got)
		}
	}
}

// The live ledger, read-only, against a hand-written posting. Nothing asserts
// on its contents beyond sanity: the log is for eyeballing the plan.
func TestLiveLedgerSmoke(t *testing.T) {
	home, _ := os.UserHomeDir()
	l, err := ledger.Load(filepath.Join(home, "projects/snipe-cli/batch/state/cv-ledger.json"))
	if err != nil || len(l.Entries) == 0 {
		t.Skip("no live ledger")
	}
	var sagaText, deployText, svcsText string
	for _, e := range l.Entries {
		for _, b := range e.Bullets {
			switch {
			case e.ID == "de-store" && strings.Contains(b.Text, "6-step saga"):
				sagaText = b.Text
			case e.ID == "de-store" && strings.Contains(b.Text, "10 Java and Spring Boot"):
				svcsText = b.Text
			case e.ID == "ubwis" && strings.Contains(b.Text, "Terraform"):
				deployText = b.Text
			}
		}
	}
	if sagaText == "" {
		t.Skip("saga bullet not in this ledger")
	}
	p := Posting{
		ID: "smoke", Date: "2026-10-21", Score: 4.3,
		Rows: []phase2.Row{
			row("Experience designing event-driven microservices on Kafka", true, fit.Strong, "Distributed Store Management Platform: "+sagaText),
			row("Kafka-based messaging between services", true, fit.Strong, "Distributed Store Management Platform: "+svcsText),
			row("Operating services on Kubernetes", true, fit.Transferable, "Software Engineer: "+deployText),
			row("Experience with Apache Spark", false, fit.Gap, "—"),
		},
		Terms: append(terms(KindTool, "Kafka", "Kubernetes", "Spark"), terms(KindCapability, "event-driven microservices")...),
	}
	for _, text := range []string{sagaText, svcsText, deployText} {
		if text == "" {
			continue
		}
		pl := BuildPlan(l, text, p)
		if pl.Verbatim {
			t.Fatalf("live bullet not found in its own ledger: %.60s", text)
		}
		t.Logf("\nentry %s | %.70s...\n  max_lines %d  lead %q\n  targets   %+v\n  allowed   %v\n  proposed  %+v\n  forbidden %v\n  must_keep %+v",
			pl.EntryID, pl.Bullet.Text, pl.MaxLines, pl.Lead, pl.Targets, pl.AllowedTerms, pl.Proposed, pl.Forbidden, pl.MustKeep)
	}
	for _, c := range Candidates(l, PlanAll(l, []string{sagaText, svcsText, deployText}, p), p) {
		t.Logf("queue: %s %q (%s) why %q", c.EntryID, c.Fact.Text, c.Fact.Approved, c.Why)
	}
}

func TestFramingTargetsAddsUnalignedRequirements(t *testing.T) {
	rows := []phase2.Row{{Requirement: "Experience with microservices at scale", MustHave: true, Strength: fit.Strong}}
	got := framingTargets(nil, Posting{Rows: rows}, ledger.Framing{Phrase: "microservices", From: []string{"x/1a"}, Approved: true})
	if len(got) != 1 || got[0].Fact != "x/1a" || !got[0].Must {
		t.Fatalf("targets = %+v", got)
	}
	if again := framingTargets(got, Posting{Rows: rows}, ledger.Framing{Phrase: "microservices"}); len(again) != 1 {
		t.Errorf("duplicate target added: %+v", again)
	}
}
