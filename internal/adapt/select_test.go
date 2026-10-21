package adapt

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/verify"
)

func sv(kind, text string) Verified {
	return Verified{Draft: Draft{Kind: kind, Text: text}, Class: verify.Supported}
}

func iv(kind, text string) Verified {
	v := sv(kind, text)
	v.Class = verify.Inferred
	return v
}

// selPlan is a plan for svcs with the targets and terms a test cares about.
func selPlan(targets []Target, allowed ...string) Plan {
	b := bullet(svcs, []ledger.Fact{
		{ID: "shop/2a", Text: "Built retail operations as 10 Java and Spring Boot services", Entities: []string{"Java", "Spring Boot"}},
		{ID: "shop/2b", Text: "Each service owns its own database"},
		{ID: "shop/2c", Text: "Services talk to each other only through a Kafka log", Entities: []string{"Kafka"}},
	}, ledger.Framing{Phrase: "event-driven microservices", From: []string{"shop/2a", "shop/2c"}, Approved: true})
	return Plan{EntryID: "shop", Bullet: b, Targets: targets, AllowedTerms: allowed}
}

func TestSelectTieKeepsOriginal(t *testing.T) {
	// Same words in the same places: nothing is gained, so nothing changes.
	p := selPlan([]Target{{Requirement: "Kafka experience", Must: true}}, "Kafka")
	got := Select(p, svcs, []Verified{sv("A", strings.Replace(svcs, "Built", "Created", 1)), sv("B", svcs+" today")}, Tally{})
	if got.Source != "original" || got.Text != svcs {
		t.Errorf("got %s %q, want the original", got.Source, got.Text)
	}
}

func TestSelectFrontLoadedBeatsMinimalEdit(t *testing.T) {
	p := selPlan([]Target{{Requirement: "Kafka experience", Must: true}}, "Kafka")
	a := strings.Replace(svcs, "a Kafka log", "Kafka", 1) // Kafka still at the end
	b := "Kafka-based " + svcs                            // Kafka first
	got := Select(p, svcs, []Verified{sv("A", a), sv("B", b)}, Tally{})
	if got.Source != "B" || got.Text != b {
		t.Errorf("got %s %q, want B", got.Source, got.Text)
	}
}

func TestSelectOrderDoesNotMatter(t *testing.T) {
	p := selPlan([]Target{
		{Requirement: "Kafka experience", Must: true},
		{Requirement: "Spring Boot", Must: false},
	}, "Kafka", "Spring Boot")
	drafts := []Verified{
		sv("A", "Spring Boot services for retail, "+svcs),
		sv("B", "Kafka services in Spring Boot for retail"),
		sv("B", "Kafka and Spring Boot services for retail"),
		sv("A", "Kafka and Spring Boot services for retail"), // same text, other kind
		iv("A", "Kafka services on Spring Boot"),
	}
	want := Select(p, svcs, drafts, Tally{})
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 50; i++ {
		sh := append([]Verified(nil), drafts...)
		rng.Shuffle(len(sh), func(i, j int) { sh[i], sh[j] = sh[j], sh[i] })
		if got := Select(p, svcs, sh, Tally{}); !reflect.DeepEqual(got, want) {
			t.Fatalf("order changed the choice:\n got %+v\nwant %+v", got, want)
		}
	}
	if want.Source == "original" {
		t.Errorf("fixture is meant to have a winner, got the original: %s", want.Why)
	}
}

// inferred is a Scope with one approved inference, Kubernetes, for the entry.
func k8s() Scope {
	return Scope{Approved: []ledger.Fact{{ID: "shop/9k", Text: "Used Kubernetes", Entities: []string{"Kubernetes"}, Source: "inferred", Status: "approved"}}}
}

func TestSelectBudgetSkipsInferredDraft(t *testing.T) {
	p := selPlan([]Target{{Requirement: "Kubernetes", Must: true}}, "Kubernetes")
	k := "Kubernetes services in Java and Spring Boot, " + svcs
	// The role has 3 claims in this bullet and 1 inferred already: a second
	// inferred would be 2 of 6.
	full := Tally{Supported: 5, Inferred: 1, Scope: k8s()}
	got := Select(p, svcs, []Verified{iv("B", k)}, full)
	if got.Source != "original" || !strings.Contains(got.Why, "20%") {
		t.Errorf("got %s (%s), want the original with the budget as the reason", got.Source, got.Why)
	}
	// A supported draft that wins is not blocked by a full budget.
	got = Select(p, svcs, []Verified{iv("B", k), sv("A", "Kubernetes-ready "+svcs)}, full)
	if got.Source != "A" {
		t.Errorf("got %s, want the supported A", got.Source)
	}
	// With room it ships, and says what it cost.
	roomy := Tally{Supported: 12, Scope: k8s()}
	got = Select(p, svcs, []Verified{iv("B", k)}, roomy)
	if got.Source != "B" || got.Claims.Inferred != 1 || !reflect.DeepEqual(got.Claims.InferredUsed, []string{"shop/9k"}) {
		t.Errorf("got %+v, want B with the inference charged", got)
	}
}

// Five bullets, 15 facts. Each wins an inferred draft; the budget stops the
// fourth: 3/18 = 16.7% is fine, 4/19 = 21.1% is not.
func TestInferredShareArithmetic(t *testing.T) {
	var bullets []ledger.Bullet
	var plans []Plan
	var survivors [][]Verified
	for i, n := range []int{3, 3, 2, 3, 4} {
		text := fmt.Sprintf("Ran job number %d across the platform every night", i)
		var facts []ledger.Fact
		for j := 0; j < n; j++ {
			facts = append(facts, ledger.Fact{ID: fmt.Sprintf("ops/%d%c", i, 'a'+j), Text: fmt.Sprintf("fact %d.%d", i, j)})
		}
		b := bullet(text, facts)
		bullets = append(bullets, b)
		plans = append(plans, Plan{
			EntryID: "ops", Bullet: b, AllowedTerms: []string{"Kubernetes"},
			Targets: []Target{{Requirement: "Kubernetes", Must: true, Strength: fit.Transferable}},
		})
		survivors = append(survivors, []Verified{iv("B", "Kubernetes: "+text)})
	}
	hold := ledger.Bullet{Hash: ledger.InferredHash, Facts: []ledger.Fact{
		{ID: "ops/9k", Text: "Used Kubernetes", Entities: []string{"Kubernetes"}, Source: "inferred", Status: "approved"},
	}}
	l := &ledger.Ledger{Entries: []ledger.Entry{{ID: "ops", Bullets: append(bullets, hold)}}}

	choices := SelectAll(l, plans, survivors)
	var src []string
	for _, c := range choices {
		src = append(src, c.Source)
	}
	if want := []string{"B", "B", "B", "original", "original"}; !reflect.DeepEqual(src, want) {
		t.Fatalf("sources = %v, want %v", src, want)
	}
	acct := Account(choices)
	if len(acct) != 1 || acct[0].Supported != 15 || acct[0].Inferred != 3 || acct[0].Unapproved != 0 {
		t.Fatalf("account = %+v, want 15 supported, 3 inferred", acct)
	}
	if got := acct[0].InferredShare(); got < 0.166 || got > 0.167 {
		t.Errorf("inferred share = %v, want 3/18", got)
	}
	if got := acct[0].SupportedShare(); got < 0.833 || got > 0.834 {
		t.Errorf("supported share = %v, want 15/18", got)
	}
	if err := Check(acct); err != nil {
		t.Errorf("Check: %v", err)
	}
	if !reflect.DeepEqual(acct[0].InferredUsed, []string{"ops/9k"}) {
		t.Errorf("inferred used = %v", acct[0].InferredUsed)
	}
}

func TestCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		e    EntryTruth
		fail bool
	}{
		"exactly 20% passes": {EntryTruth{Entry: "a", Supported: 4, Inferred: 1}, false},
		"over 20% fails":     {EntryTruth{Entry: "a", Supported: 3, Inferred: 1}, true},
		"unapproved fails":   {EntryTruth{Entry: "a", Supported: 40, Unapproved: 1}, true},
		"all supported":      {EntryTruth{Entry: "a", Supported: 7}, false},
		"empty role":         {EntryTruth{Entry: "a"}, false},
	} {
		if err := Check([]EntryTruth{tc.e}); (err != nil) != tc.fail {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// One bad role fails the offer even when the others are clean.
	if Check([]EntryTruth{{Entry: "a", Supported: 9}, {Entry: "b", Supported: 1, Inferred: 1}}) == nil {
		t.Error("a bad second role passed")
	}
}

// A class the selector would never hand over is charged as unapproved, so the
// kill rule still fires if a caller wires the wrong thing in.
func TestUnapprovedIsKilled(t *testing.T) {
	p := selPlan(nil)
	c := Choice{Entry: "shop", Claims: claimsOf(p, "x", verify.Dropped, svcs, Scope{})}
	acct := Account([]Choice{c})
	if acct[0].Unapproved != 1 || Check(acct) == nil {
		t.Errorf("account = %+v, err = %v", acct, Check(acct))
	}
}

func TestDroppedNeverSelected(t *testing.T) {
	p := selPlan([]Target{{Requirement: "Kafka", Must: true}}, "Kafka")
	got := Select(p, svcs, []Verified{{Draft: Draft{Kind: "B", Text: "Kafka " + svcs}, Class: verify.Dropped}}, Tally{})
	if got.Source != "original" {
		t.Errorf("shipped a dropped draft: %+v", got)
	}
}

func TestParityBreaksEvidenceTie(t *testing.T) {
	text := "Ran the billing store on Postgres every night"
	b := bullet(text, []ledger.Fact{{ID: "x/1a", Text: "Ran the billing store on Postgres", Entities: []string{"PostgreSQL"}}})
	p := Plan{EntryID: "x", Bullet: b, AllowedTerms: []string{"PostgreSQL"}}
	s := Scope{Forms: map[string][]string{"PostgreSQL": {"PostgreSQL", "Postgres"}}}
	got := Select(p, text, []Verified{sv("A", "Ran the billing store on PostgreSQL every night")}, Tally{Scope: s})
	if got.Source != "A" {
		t.Errorf("got %s (%s), want the spelling the posting uses", got.Source, got.Why)
	}
	// A spelling for something the line never names is not parity.
	p.AllowedTerms = []string{"MySQL"}
	got = Select(p, text, []Verified{sv("A", "Ran the billing store on MySQL every night")}, Tally{Scope: s})
	if got.Source != "original" {
		t.Errorf("got %s, want the original", got.Source)
	}
}

// PRD §6.7: the posting asks for event-driven microservices on Kafka. B is
// re-led, A is the minimal edit, and the original comes last. "Event-driven
// microservices" is an approved framing of facts 2a and 2c, so it is allowed;
// the posting also asks for Kafka by name, which only B brings forward.
func TestWorkedExample(t *testing.T) {
	targets := []Target{
		{Requirement: "experience designing event-driven microservices", Must: true},
		{Requirement: "Kafka", Must: false},
	}
	a := Verified{Draft: Draft{Kind: "A", Text: "Built 10 event-driven Java and Spring Boot microservices for retail operations, each owning its own database and talking to the others only through a Kafka log"}, Class: verify.Supported}
	b := Verified{Draft: Draft{Kind: "B", Text: "Designed 10 event-driven Kafka microservices in Java and Spring Boot for retail operations, each owning its own database"}, Class: verify.Supported}
	for name, tc := range map[string]struct {
		allowed   []string
		survivors []Verified
		want      string
	}{
		"B beats A beats original": {[]string{"event-driven microservices", "Kafka"}, []Verified{a, b}, "B"},
		"B fails verification":     {[]string{"event-driven microservices", "Kafka"}, []Verified{a}, "A"},
		"both fail":                {[]string{"event-driven microservices", "Kafka"}, nil, "original"},
		"framing not allowed":      {[]string{"Kafka"}, []Verified{a}, "original"},
	} {
		p := selPlan(targets, tc.allowed...)
		got := Select(p, svcs, tc.survivors, Tally{})
		if got.Source != tc.want {
			t.Errorf("%s: got %s (%s), want %s", name, got.Source, got.Why, tc.want)
			continue
		}
		if got.Claims.Supported != 3 || got.Claims.Inferred != 0 {
			t.Errorf("%s: claims = %+v, want 3 supported", name, got.Claims)
		}
	}
}

func TestFramingCreditedOnlyWhenWordedThatWay(t *testing.T) {
	p := selPlan(nil, "event-driven microservices")
	c := claimsOf(p, "Built 10 event-driven microservices on Kafka", verify.Supported, svcs, Scope{})
	if !reflect.DeepEqual(c.Framings, []string{"event-driven microservices"}) {
		t.Errorf("framings = %v", c.Framings)
	}
	if c := claimsOf(p, svcs, verify.Supported, svcs, Scope{}); len(c.Framings) != 0 {
		t.Errorf("the original uses no framing, got %v", c.Framings)
	}
}

func TestTruthRoundTrip(t *testing.T) {
	p := selPlan([]Target{{Requirement: "event-driven microservices", Must: true}}, "event-driven microservices")
	shipped := "Designed 10 event-driven microservices in Java and Spring Boot, each owning its own database, talking only through a Kafka log"
	c1 := Select(p, svcs, []Verified{sv("B", shipped)}, Tally{})
	c2 := Select(selPlan(nil), "Another line", nil, Tally{})
	c2.Entry = "shop"
	if c1.Source != "B" {
		t.Fatalf("setup: %+v", c1)
	}
	want := NewTruthFile("343", []Choice{c1, c2})
	path := filepath.Join(t.TempDir(), "output", "2026-10-21_acme_343", TruthName)
	if err := WriteTruth(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTruth(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}
	raw, _ := os.ReadFile(path)
	for _, s := range []string{`"supported_share"`, `"inferred_share"`, `"kind": "framing"`, `"event-driven microservices"`, `"kind": "verbatim"`} {
		if !strings.Contains(string(raw), s) {
			t.Errorf("truth.json lacks %s:\n%s", s, raw)
		}
	}
	// A rebuild replaces the file whole.
	if err := WriteTruth(path, TruthFile{Offer: "343"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadTruth(path); len(got.Bullets) != 0 {
		t.Errorf("old bullets survived a rewrite: %+v", got.Bullets)
	}
	if ents, _ := os.ReadDir(filepath.Dir(path)); len(ents) != 1 {
		t.Errorf("temp files left behind: %v", ents)
	}
}

// A framing the user approved earns parity wherever it lands in the line, so a
// draft that says "event-driven microservices" late still beats the original.
func TestParityCreditsApprovedFramingAnywhere(t *testing.T) {
	p := selPlan(nil, "event-driven microservices")
	late := svcs + " in an event-driven microservices design"
	got := Select(p, svcs, []Verified{sv("A", late)}, Tally{})
	if got.Source != "A" {
		t.Errorf("got %s %q, want A", got.Source, got.Text)
	}
	// An allowed term that is no framing and names no entity of the line is not credit.
	p2 := selPlan(nil, "observability")
	if got := Select(p2, svcs, []Verified{sv("A", svcs+" with observability")}, Tally{}); got.Source != "original" {
		t.Errorf("unframed term won: %s", got.Source)
	}
}
