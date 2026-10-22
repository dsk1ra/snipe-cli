package fit

// The three caps that rest on word lists, applied to facts a model extracted
// and quoted (internal/extract) instead of to regexes over the posting. Only the
// reading of the posting changes. The policy is the regex versions' policy and
// the profile.yml tiers are the same ones (commuteTiers, CandidateLanguages), so
// a disagreement between the two is a reading difference to be looked at, not a
// second policy.
//
// The functions read values only. extract.Verify has already turned a fact whose
// quote is not in the posting into an unknown (mode "unspecified", zeros, no
// language), and an unknown never fires a cap — the direction the regexes err in.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/extract"
)

// placeHas reports whether a place as the posting writes it ("Central London,
// near Liverpool Street", "Birmingham or Coventry") contains a profile term as a
// whole word. This replaces ukCities: the profile says where the candidate will
// go, and the posting's own place is compared to that, so no list of places the
// candidate will not go is needed.
//
// Whole words, as the regex path matched them, so "bath" is not "Bathgate". A
// term that is itself the tail of a longer name ("york" in "New York") still
// matches; the regexes had the same weakness and the profile's terms are the
// candidate's own towns.
func placeHas(place, term string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(term) + `\b`).MatchString(place)
}

// LocationCapFrom is LocationMismatchCap over an attendance fact. The second
// result is the place that capped, lower-cased, where the regex path returns the
// city.
//
// The policy, step for step as in LocationMismatchCap:
//   - no commutable place in the profile: no gate;
//   - the posting asks for no attendance (remote, or nothing stated): no cap.
//     "remote" covers the fullyRemote pattern; a posting extracted as both
//     hybrid and remote is whatever mode the model chose;
//   - the place is a full-time base: no cap;
//   - a monthly cadence with no weekly one is inside the travel policy
//     ("6 days a month travel to office");
//   - the place is hybrid-tier ("London — hybrid or remote"): capped only for
//     full-time attendance (five days a week, or on-site with no days stated);
//   - any other named place caps.
//
// Where it differs from the regex path on purpose: the regexes capped only a
// city in the 29-city list and only within 160 characters of the signal, so
// "Berlin, hybrid" and "Reading, hybrid" were handled by luck of the list. Here
// any place the posting names for this role that is not the candidate's caps,
// and a posting that names none ("Hybrid working available") does not, as
// before. The proximity rule has no equivalent: the model is asked for the
// attendance of the role, not for every mention, which is what proximity
// approximated.
func LocationCapFrom(a extract.Attendance, profileYML string) (Caps, string) {
	none := Caps{CV: 5, NS: 5}
	full, hybridOK := commuteTiers(profileYML)
	if len(full) == 0 && len(hybridOK) == 0 {
		return none, ""
	}
	weekly, monthly := a.DaysPerWeek, a.DaysPerMonth
	attends := a.Mode == extract.Onsite || a.Mode == extract.Hybrid || weekly > 0 || monthly > 0
	place := strings.ToLower(strings.TrimSpace(a.Place))
	if a.Mode == extract.Remote || !attends || place == "" {
		return none, ""
	}
	for _, t := range full {
		if placeHas(place, t) {
			return none, ""
		}
	}
	if monthly > 0 && weekly == 0 {
		return none, ""
	}
	for _, t := range hybridOK {
		if placeHas(place, t) {
			if weekly >= 5 || (weekly == 0 && a.Mode == extract.Onsite) {
				break // full-time attendance outranks the word hybrid
			}
			return none, ""
		}
	}
	return Caps{CV: 2, NS: 2}, place
}

// LanguageCapFrom is LanguageMismatchCap over the extracted language
// requirements. A "preferred" language never caps; that classification replaces
// softMentionRe, which looked 60 characters either side for "plus", "bonus" and
// the like.
//
// The regex path only knew the 20 languages in humanLanguages. This takes any
// language the posting requires and the CV does not list, so a Hindi or Greek
// requirement caps where it was invisible before. English never caps, as before:
// CandidateLanguages always has it.
func LanguageCapFrom(langs []extract.Language, cv string) (Caps, string) {
	have := CandidateLanguages(cv)
	for _, l := range langs {
		name := strings.ToLower(strings.TrimSpace(l.Language))
		if l.Requirement == extract.Required && name != "" && !have[name] {
			return Caps{CV: 2, NS: 2}, name
		}
	}
	return Caps{CV: 5, NS: 5}, ""
}

// CapDiff describes where the word-list cap and the extracted-fact cap differ,
// for the shadow field on a score file. Empty when they agree.
func CapDiff(name string, old, new Caps, oldWhy, newWhy string) string {
	if old.CV == new.CV && old.NS == new.NS {
		return ""
	}
	return fmt.Sprintf("%s: lists cap %d/%d %s, facts cap %d/%d %s", name, old.CV, old.NS, oldWhy, new.CV, new.NS, newWhy)
}

// StackCapFrom is StackMismatchCap over the extracted stack. The posting's
// required programming languages are compared with the languages the CV
// demonstrates, both in the canonical form the extraction prompts give, so the
// ecosystem table is not needed. No required language, or any one the CV has,
// is no cap, as before; preferred languages never cap, which replaces the
// two-mentions rule.
func StackCapFrom(stack []extract.StackItem, cvLangs []string) StackCap {
	have := map[string]bool{}
	for _, l := range cvLangs {
		have[l] = true
	}
	var present []string
	for _, it := range stack {
		if it.Requirement == extract.Required && !slices.Contains(present, it.Language) {
			present = append(present, it.Language)
		}
	}
	if len(present) == 0 {
		return StackCap{Cap: 5}
	}
	for _, p := range present {
		if have[p] {
			return StackCap{Cap: 5, JDStack: present}
		}
	}
	return StackCap{Cap: 3, JDStack: present, Missing: present}
}
