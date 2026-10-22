package fit

import (
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/extract"
)

// Each row is a posting, the facts a correct reading of it gives, and whether
// the cap fires. The same posting goes through the regex path and the facts
// path and both have to say want: the facts path changes how the posting is
// read, not what is decided. Most postings are the ones fit_test.go pins, each
// of which is a bug that shipped.

const extProfile = `location:
  city: "Ashcombe"
  search_locations:
    - "Ashcombe / Mereside — on-site, hybrid or remote"
    - "London — hybrid or remote"
    - "United Kingdom — fully remote only"
`

func att(mode, place string, week, month int) extract.Attendance {
	return extract.Attendance{Mode: mode, Place: place, DaysPerWeek: week, DaysPerMonth: month}
}

func TestLocationCapFrom(t *testing.T) {
	for _, tc := range []struct {
		name, jd, prof string
		a              extract.Attendance
		want           bool
	}{
		{"hybrid in a hybrid-tier city", "Location: London (Hybrid Working). Rust as a main language.", extProfile,
			att("hybrid", "London", 0, 0), false},
		{"three days a week in a hybrid-tier city", "Location & Work Model: London, UK | 3 days per week in the office", extProfile,
			att("hybrid", "London, UK", 3, 0), false},
		{"words between days and week", "Location: London, UK (2 Days Onsite Per Week)", extProfile,
			att("hybrid", "London, UK", 2, 0), false},
		{"five days on-site is not hybrid", "Location: Gloucester, Manchester or London (5 days onsite)", extProfile,
			att("onsite", "Gloucester, Manchester or London", 5, 0), true},
		{"on-site with no days stated", "Location: London. This role is on-site.", extProfile,
			att("onsite", "London", 0, 0), true},
		{"hybrid elsewhere is still capped", "Location: Glasgow. Work Setup: Hybrid - 3 days in the office", extProfile,
			att("hybrid", "Glasgow", 3, 0), true},
		{"an acceptable option among others", "Location: London or Manchester – Hybrid working offered", extProfile,
			att("hybrid", "London or Manchester", 0, 0), false},
		{"hybrid for a year then five days is full time", "London. Hybrid for the first year, then 5 days a week in the office.", extProfile,
			att("hybrid", "London", 5, 0), true},
		// "Liverpool Street" broke an earlier list: the station read as Liverpool.
		// The model gives the place of the role, which is London.
		{"a London station is not Liverpool", "Our team is based in central London, near Liverpool Street station. Hybrid working model.", extProfile,
			att("hybrid", "central London", 0, 0), false},
		{"the commutable base is never capped", "Backend Engineer, Ashcombe. Hybrid, 3 days a week in the office.", extProfile,
			att("hybrid", "Ashcombe", 3, 0), false},

		// London is remote-only in this profile.
		{"a hybrid role in a city we will not commute to", "Location: London (Hybrid Working). Rust as a main language.", profileYML,
			att("hybrid", "London", 0, 0), true},
		{"weekly commute", "Location & Work Model: London, UK | Hybrid (3 days per week in the office)", profileYML,
			att("hybrid", "London, UK", 3, 0), true},
		{"near-adjacent signals still cap", "Location: London. Hybrid working, 2 days a week in the office.", profileYML,
			att("hybrid", "London", 2, 0), true},
		// "6 days a month" broke an earlier version: it said hybrid and was capped.
		{"a monthly cadence is inside the travel policy", "Locations: London, Essex | Hybrid: 6 days a month travel to office", profileYML,
			att("hybrid", "London, Essex", 0, 6), false},
		{"fully remote naming a city is not capped", "Fully remote (UK). The team meets in London twice a year.", profileYML,
			att("remote", "", 0, 0), false},
		{"an on-site signal naming no city", "Backend Engineer. Hybrid working available.", profileYML,
			att("hybrid", "", 0, 0), false},
		// The regexes needed a proximity rule to get this; the model is asked about
		// the role, and a posting that has no in-office requirement says so.
		{"no in-office requirement, London among global offices", "There is no minimum in-office qualification requirement. " + strings.Repeat("x", 400) + " Headquartered overseas with key offices in London, New York and Paris.", profileYML,
			att("unspecified", "", 0, 0), false},
		{"no stated policy, no gate", "Location: London (Hybrid)", "",
			att("hybrid", "London", 0, 0), false},
	} {
		_, oldCity := LocationMismatchCap(tc.jd, tc.prof)
		caps, place := LocationCapFrom(tc.a, tc.prof)
		if got := oldCity != ""; got != tc.want {
			t.Errorf("%s: the regex path says %v (%q), the table says %v", tc.name, got, oldCity, tc.want)
		}
		if got := place != ""; got != tc.want {
			t.Errorf("%s: facts path says %v (%q), want %v", tc.name, got, place, tc.want)
		}
		if w := map[bool]int{true: 2, false: 5}[tc.want]; caps.CV != w || caps.NS != w {
			t.Errorf("%s: caps %d/%d, want %d/%d", tc.name, caps.CV, caps.NS, w, w)
		}
	}
}

// Where the facts path is meant to differ: a place outside the 29-city list.
func TestLocationCapFromBeyondTheCityList(t *testing.T) {
	jd := "Location: Berlin. Hybrid, 3 days a week in the office."
	if _, city := LocationMismatchCap(jd, profileYML); city != "" {
		t.Fatalf("the regex path capped %q; Berlin is not in ukCities", city)
	}
	if _, place := LocationCapFrom(att("hybrid", "Berlin", 3, 0), profileYML); place != "berlin" {
		t.Errorf("got %q, want berlin", place)
	}
	// A whole word, as the regexes matched: Bath is not Bathgate.
	if _, place := LocationCapFrom(att("onsite", "Ashcombe", 0, 0), "location:\n  city: \"Ash\"\n"); place == "" {
		t.Error("\"ash\" matched inside \"ashcombe\"")
	}
}

func TestLanguageCapFrom(t *testing.T) {
	lang := func(l, r, q string) []extract.Language {
		return []extract.Language{{Language: l, Requirement: r, Quote: q}}
	}
	for _, tc := range []struct {
		name, jd string
		langs    []extract.Language
		want     string
	}{
		{"german-speaking role capped", "Software Engineer, Agent (German speaking). Professional fluency in both German and English.",
			append(lang("German", "required", "German speaking"), lang("English", "required", "fluency in both German and English")...), "german"},
		{"a language the candidate has is fine", "Fluency in Spanish required for client calls",
			lang("Spanish", "required", "Fluency in Spanish required"), ""},
		{"nice-to-have is not a requirement", "German is a plus but not required. Fluent German would be a bonus.",
			lang("German", "preferred", "Fluent German would be a bonus"), ""},
		{"naming a market is not demanding a language", "We serve the German market from our Berlin office.", nil, ""},
		// C#.Net broke the stack cap, not this one; the point here is that the
		// posting names no human language.
		{"a C#.Net posting names no language", "C#.Net developer. Strong C# and .NET Core throughout.", nil, ""},
		{"a language outside the old list", "Fluent Hindi is required.", lang("Hindi", "required", "Fluent Hindi is required"), "hindi"},
	} {
		_, old := LanguageMismatchCap(tc.jd, langCV)
		caps, got := LanguageCapFrom(tc.langs, langCV)
		if tc.name != "a language outside the old list" && old != tc.want {
			t.Errorf("%s: the regex path says %q, the table says %q", tc.name, old, tc.want)
		}
		if got != tc.want {
			t.Errorf("%s: facts path says %q, want %q", tc.name, got, tc.want)
		}
		if w := map[bool]int{true: 2, false: 5}[tc.want != ""]; caps.CV != w || caps.NS != w {
			t.Errorf("%s: caps %d/%d, want %d/%d", tc.name, caps.CV, caps.NS, w, w)
		}
	}
}

func TestStackCapFrom(t *testing.T) {
	req := func(lang string) extract.StackItem {
		return extract.StackItem{Language: lang, Requirement: extract.Required}
	}
	pref := extract.StackItem{Language: "python", Requirement: extract.Preferred}
	for _, tc := range []struct {
		name  string
		stack []extract.StackItem
		cv    []string
		want  int
	}{
		{"no language named", nil, []string{"go"}, 5},
		{"covered", []extract.StackItem{req("python"), req("scala")}, []string{"python"}, 5},
		{"none covered", []extract.StackItem{req("swift"), req("scala")}, []string{"go", "python"}, 3},
		{"preferred never caps", []extract.StackItem{pref}, []string{"go"}, 5},
		{"required beside preferred", []extract.StackItem{pref, req("swift")}, []string{"python"}, 3},
	} {
		if got := StackCapFrom(tc.stack, tc.cv); got.Cap != tc.want {
			t.Errorf("%s: cap %d, want %d", tc.name, got.Cap, tc.want)
		}
	}
}
