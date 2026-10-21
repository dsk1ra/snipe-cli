package suggest

import (
	"encoding/json"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/fill"
)

const cv = "Built REST services handling 40k req/s on Postgres. Led a team of 4 on Kafka pipelines."

func content() fill.Content {
	c := fill.Content{Experience: []fill.ContentExp{{Company: "Acme", Bullets: []string{
		"Built REST services handling 40k req/s on Postgres",
		"Led a team of 4 on Kafka pipelines",
	}}}}
	c.Skills, _ = json.Marshal([]map[string]string{{"category": "Data", "items": "PostgreSQL, Kafka (Streams, Connect)"}})
	return c
}

func TestTargetsNumberInPageOrder(t *testing.T) {
	var ids []string
	for _, tg := range Targets(content()) {
		ids = append(ids, tg.ID)
	}
	if got := len(ids); got != 4 || ids[0] != "B1" || ids[2] != "K1" || ids[3] != "K2" {
		t.Errorf("ids %v", ids)
	}
}

func TestVet(t *testing.T) {
	jd := "We want RESTful API services on Postgres, with Kafka and Terraform. Mentoring is a plus."
	tg := Targets(content())
	for name, tc := range map[string]struct {
		p    proposal
		keep bool
	}{
		"takes the posting's wording":      {proposal{"B1", "Built RESTful API services handling 40k req/s on Postgres", "RESTful APIs"}, true},
		"drops a figure":                   {proposal{"B1", "Built RESTful API services on Postgres", ""}, false},
		"invents a figure":                 {proposal{"B1", "Built RESTful API services handling 90k req/s on Postgres", ""}, false},
		"names a product the CV lacks":     {proposal{"B2", "Led a team of 4 on Kafka and Terraform pipelines", ""}, false},
		"brings in nothing from the JD":    {proposal{"B2", "Headed a group of 4 on Kafka pipelines", ""}, false},
		"runs far past the original":       {proposal{"B2", "Led a team of 4 on Kafka pipelines, mentoring engineers and running RESTful API reviews every sprint across squads", ""}, false},
		"tacks an outcome on the end":      {proposal{"B2", "Led a team of 4 on Kafka pipelines for RESTful services", ""}, false},
		"unknown id":                       {proposal{"B9", "x", ""}, false},
		"skill respelt as the posting has": {proposal{"K1", "Postgres", ""}, true},
		"skill the posting never says":     {proposal{"K1", "PostgresDB", ""}, false},
		"skill that is two":                {proposal{"K1", "Postgres, Terraform", ""}, false},
		"skill that is a product not held": {proposal{"K2", "Terraform", ""}, false},
	} {
		if got := len(vet(tg, []proposal{tc.p}, cv, jd)) == 1; got != tc.keep {
			t.Errorf("%s: kept=%v want %v", name, got, tc.keep)
		}
	}
}

func TestApplyOnlyTakesAcceptedAndLeavesTheOriginal(t *testing.T) {
	c := content()
	items := []Item{
		{ID: "B1", Kind: Bullet, Old: c.Experience[0].Bullets[0], New: "Built RESTful API services", State: Accepted},
		{ID: "B2", Kind: Bullet, Old: c.Experience[0].Bullets[1], New: "never", State: Rejected},
		{ID: "K1", Kind: Skill, Old: "PostgreSQL", New: "Postgres", State: Accepted},
		{ID: "K9", Kind: Skill, Old: "gone since", New: "x", State: Accepted},
	}
	got, n := Apply(c, items)
	if n != 2 {
		t.Errorf("applied %d, want 2", n)
	}
	if b := got.Experience[0].Bullets; b[0] != "Built RESTful API services" || b[1] != c.Experience[0].Bullets[1] {
		t.Errorf("bullets %q", b)
	}
	if r := got.SkillRows()[0].Items; r != "Postgres, Kafka (Streams, Connect)" {
		t.Errorf("skills %q", r)
	}
	if c.Experience[0].Bullets[0] != "Built REST services handling 40k req/s on Postgres" {
		t.Error("Apply wrote through to the input")
	}
	if a := Approved(items); len(a) < 1 || a[0] != "Postgres" {
		t.Errorf("approved %v", a)
	}
}

func TestMarkSurvivesAReload(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, []Item{{ID: "B1", Old: "a", New: "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := Mark(dir, "B1", Accepted); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(dir); len(got) != 1 || got[0].State != Accepted {
		t.Errorf("%+v", got)
	}
	if Mark(dir, "B7", Accepted) == nil {
		t.Error("marked an id that is not there")
	}
}
