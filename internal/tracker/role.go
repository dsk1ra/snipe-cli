package tracker

import (
	"regexp"
	"strings"
)

// Fuzzy role matching, ported from tracker/role-matcher.mjs.
//
// It answers one question for the merge: are these two titles the same opening?
// Getting it wrong in either direction is expensive — a false match folds a new
// application into an unrelated row, a false miss files the same offer twice.

// roleStopwords are words that appear in titles without identifying the
// opening: seniority, work mode, contract shape, and the cities that leak into
// job titles.
var roleStopwords = set(
	"junior", "mid", "middle", "senior", "staff", "principal", "lead", "head",
	"chief", "associate", "intern", "entry", "level",
	"remote", "hybrid", "onsite", "contract", "contractor", "freelance",
	"fulltime", "parttime", "permanent", "temporary", "internship",
	"role", "position", "opportunity", "team", "based",
	"bangalore", "bengaluru", "mumbai", "delhi", "hyderabad", "pune", "chennai",
	"london", "berlin", "paris", "madrid", "barcelona", "amsterdam", "dublin",
	"york", "francisco", "seattle", "boston", "austin", "chicago", "toronto",
	"tokyo", "singapore", "sydney", "melbourne", "lisbon", "warsaw",
	"europe", "emea", "apac", "latam", "americas", "india", "spain", "germany",
	"france", "italy", "canada", "brazil", "mexico", "japan",
	"with", "from", "into", "over", "this", "that",
)

// shortSpecialty are acronyms that discriminate despite being short. Broad
// two-letter buckets like AI/ML are deliberately absent — they appear across
// many unrelated roles.
var shortSpecialty = set(
	"api", "sre", "sdk", "cli", "gpu", "cpu",
	"ios", "qa", "ux", "ui", "ar", "vr",
	"ocr", "crm", "erp",
)

// baselineTokens are generic role descriptors. Two titles whose only overlap is
// in this set are not the same opening; they are written at the same altitude.
var baselineTokens = set(
	"software", "engineer", "developer", "manager", "architect",
	"analyst", "designer", "consultant", "specialist",
	"platform", "systems", "services",
	"backend", "frontend", "full", "stack", "fullstack",
)

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

var nonRoleChar = regexp.MustCompile(`[^a-z0-9\s]`)

// RoleTokens keeps long descriptive words and the short specialty acronyms,
// dropping stopwords. Baseline tokens stay in — they contribute to the ratio,
// they just cannot be the only reason two titles match.
func RoleTokens(role string) []string {
	var out []string
	for _, w := range strings.Fields(nonRoleChar.ReplaceAllString(strings.ToLower(role), " ")) {
		if (len(w) > 3 || shortSpecialty[w]) && !roleStopwords[w] {
			out = append(out, w)
		}
	}
	return out
}

// RoleFuzzyMatch decides whether two titles are likely the same opening: at
// least two shared tokens, at least one of them not merely baseline vocabulary,
// and a Jaccard overlap of 0.6 or more.
//
// Jaccard over the union rather than over the smaller title. Dividing by the
// smaller one inflates matches for roles sharing a long generic prefix and
// differing only in specialty — which is exactly the pair that must stay apart.
func RoleFuzzyMatch(a, b string) bool {
	ta, tb := uniq(RoleTokens(a)), uniq(RoleTokens(b))
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	inB := set(tb...)
	var overlap []string
	for _, w := range ta {
		if inB[w] {
			overlap = append(overlap, w)
		}
	}
	if len(overlap) < 2 {
		return false
	}
	discriminating := false
	for _, w := range overlap {
		if !baselineTokens[w] {
			discriminating = true
			break
		}
	}
	if !discriminating {
		return false
	}
	union := set(ta...)
	for _, w := range tb {
		union[w] = true
	}
	return float64(len(overlap))/float64(len(union)) >= 0.6
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, w := range in {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}
