// Package phase1 is the pre-score: fetch the posting, cache it, and score it
// cheaply on a 4B so the 30B only ever sees offers worth its time.
package phase1

import (
	"context"
	"encoding/json"
	"errors"

	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

// jdPromptCap is the JD budget inside num_ctx 12288. Fixed context (system
// prompt + cleaned CV + profile + config) is ~21k chars, about 5.5k tokens; a
// 10k-char JD is ~2.5k more and leaves ample output room.
const jdPromptCap = 10_000

// modelReply is what the schema constrains the model to.
//
// hard_stops is deliberately absent. When the model was asked for it while the
// prompt carried the profile's deal-breaker checklist, 90% of the hard stops it
// emitted (74 of 82) were verbatim copies of that checklist rather than
// findings: a role-shape deal-breaker stamped on 11 postings that plainly were
// not that shape, a salary-floor one on postings naming no salary at all. A
// small model completes a checklist it is shown rather than testing it. The
// field still exists on the output; it is filled from the deterministic gates.
type modelReply struct {
	Company       string `json:"company"`
	Role          string `json:"role"`
	CVMatch       int    `json:"cv_match"`
	NorthStar     int    `json:"north_star"`
	RedFlagsScore int    `json:"red_flags_score"`
	Archetype     string `json:"archetype"`
	// Each gap carries the verbatim JD span that proves the requirement is real.
	SoftGaps []struct {
		Requirement string `json:"requirement"`
		JDQuote     string `json:"jd_quote"`
	} `json:"soft_gaps"`
	TopStrengths []string `json:"top_strengths"`
	JDSummary    string   `json:"jd_summary"`
	Confidence   string   `json:"confidence"`
}

func scoreSchema(archetypes []string) ollama.Schema {
	dim := ollama.Int(1, 5)
	return ollama.Object(ollama.Obj(
		ollama.F("company", ollama.Str()),
		ollama.F("role", ollama.Str()),
		ollama.F("cv_match", dim),
		ollama.F("north_star", dim),
		ollama.F("red_flags_score", dim),
		ollama.F("archetype", ollama.Str(archetypes...)),
		ollama.F("soft_gaps", ollama.Arr(ollama.Object(ollama.Obj(
			ollama.F("requirement", ollama.Str()),
			ollama.F("jd_quote", ollama.Str()),
		)), 5)),
		ollama.F("top_strengths", ollama.Arr(ollama.Str(), 3)),
		ollama.F("jd_summary", ollama.Str()),
		ollama.F("confidence", ollama.Str("Low", "Medium", "High")),
	))
}

// Score is what lands in batch/scores/<id>.json.
type Score struct {
	Status    string  `json:"status"`
	ID        string  `json:"id"`
	URL       string  `json:"url"`
	JDFile    string  `json:"jd_file"`
	Model     string  `json:"model"`
	ScoredAt  string  `json:"scored_at"`
	Score     float64 `json:"score"`
	CVMatch   int     `json:"cv_match"`
	NorthStar int     `json:"north_star"`
	// Pre-cap dimensions, kept for calibration: the clamp is an attractor —
	// 42 of 115 offers land on the Senior cap's exact fixed point 3.4 — and no
	// alternative cap policy can be evaluated without the raw values.
	CVMatchRaw       int      `json:"cv_match_raw"`
	NorthStarRaw     int      `json:"north_star_raw"`
	CVCap            int      `json:"cv_cap"`
	NSCap            int      `json:"ns_cap"`
	RedFlagsScore    int      `json:"red_flags_score"`
	Company          string   `json:"company"`
	Role             string   `json:"role"`
	Archetype        string   `json:"archetype"`
	HardStops        []string `json:"hard_stops"`
	SoftGaps         []string `json:"soft_gaps"`
	TopStrengths     []string `json:"top_strengths"`
	GapsDropped      int      `json:"gaps_dropped_unverified"`
	StrengthsDropped int      `json:"strengths_dropped_unverified"`
	JDSummary        string   `json:"jd_summary"`
	Confidence       string   `json:"confidence"`
	Error            *string  `json:"error"`
}

type Scorer struct {
	L     paths.Layout
	Cfg   *config.Config
	OL    *ollama.Client
	Model string
	Store *state.Store
	// Timeout bounds one model call. The fetch gets its own, shorter budget.
	Timeout time.Duration
}

// Run scores one offer. Both stages are checkpointed, so a stop between the
// fetch and the model call does not refetch, and a restart after the model call
// does not pay for it twice.
func (s *Scorer) Run(ctx context.Context, id int, ck *state.Checkpoint) error {
	row, _ := s.Store.Get(id)
	offers, err := state.LoadOffers(s.L.Input())
	if err != nil {
		return err
	}
	url := offers[id].URL
	if url == "" {
		url = row.URL
	}
	cachePath := filepath.Join(s.L.JDs(), strconv.Itoa(id)+".txt")

	jd, err := state.Staged(ctx, ck, "p1.fetch", url, func(ctx context.Context, u string) (string, error) {
		return FetchJD(ctx, u, cachePath, 30*time.Second)
	})
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			// Record it and stop. An expired posting is not a failure to retry.
			msg := err.Error()
			s.write(id, &Score{Status: state.P1Unavailable, ID: strconv.Itoa(id), URL: url, Error: &msg})
			return s.Store.Commit(id, func(r *state.Row) {
				r.URL = url
				r.P1Status = state.P1Unavailable
				r.Error = err.Error()
			})
		}
		return err
	}
	// The cache is the durable copy every later phase reads, and what `debug`
	// opens for editing before a retry.
	if err := os.WriteFile(cachePath, []byte(jd), 0o644); err != nil {
		return err
	}

	// The model call is keyed on the JD *and* the CV: editing either must
	// re-score, and editing neither must not.
	type scoreInput struct {
		JD  string `json:"jd"`
		CV  string `json:"cv"`
		URL string `json:"url"`
	}
	in := scoreInput{JD: jd, CV: s.Cfg.Hash(), URL: url}

	reply, err := state.Staged(ctx, ck, "p1.score", in, func(ctx context.Context, in scoreInput) (modelReply, error) {
		return s.call(ctx, id, in.JD, in.URL)
	})
	if err != nil {
		return err
	}

	out := s.validate(reply, jd, url, id)
	if err := s.write(id, out); err != nil {
		return err
	}
	return s.Store.Commit(id, func(r *state.Row) {
		r.URL = url
		r.P1Status = state.P1Scored
		r.P1Score = strconv.FormatFloat(out.Score, 'f', -1, 64)
		r.Archetype = out.Archetype
		r.Error = ""
	})
}

func (s *Scorer) call(ctx context.Context, id int, jd, url string) (modelReply, error) {
	system, err := config.Prompt(s.L, "ollama-score-prompt.md")
	if err != nil {
		return modelReply{}, err
	}
	user := strings.Join([]string{
		"## Candidate CV", s.Cfg.CV, "",
		"## Candidate Target Role Profile", orNone(s.Cfg.ProfileMD, "(no profile found)"), "",
		"## Candidate Configuration", orNone(s.Cfg.ProfileYML, "(no config found)"), "",
		"## Job Description to Score", "URL: " + url, "",
		text.CleanJD(jd, jdPromptCap), "",
		"Score this job offer for the candidate. Output only the JSON object, no other text.",
	}, "\n")

	var reply modelReply
	err = s.OL.Chat(ctx, ollama.Request{
		Model: s.Model, System: system, User: user,
		Schema:  scoreSchema(s.Cfg.ArchetypeEnum()),
		Options: ollama.Options{Temperature: 0, NumCtx: 12288},
		Timeout: s.Timeout, Label: "p1.score", ID: id,
	}, &reply)
	return reply, err
}

// validate computes the composite in code and applies the deterministic caps.
//
// The model's own self-reported score is never trusted: it collapses toward the
// centre on small models. red_flags is excluded from the formula — the 4B
// hallucinates deal-breakers — and comp is not collected at Phase 1 at all,
// since with no salary data it was a dead constant. The score is pure fit, in
// the same 5:3 ratio Phase 2 uses, so the two phases stay consistent.
func (s *Scorer) validate(m modelReply, jd, url string, id int) *Score {
	cvRaw, nsRaw := clampDim(m.CVMatch, 3), clampDim(m.NorthStar, 3)

	stack := fit.StackMismatchCap(jd, s.Cfg.CVRaw, 0, 0)
	lang, missingLang := fit.LanguageMismatchCap(jd, s.Cfg.CVRaw)
	loc, city := fit.LocationMismatchCap(jd, s.Cfg.ProfileYML)
	// The same seniority caps Phase 2 applies. Benchmarked: every tested model
	// overscored Senior-titled roles at Phase 1, so this cannot live in a prompt.
	sen := fit.SeniorityCaps(m.Role, jd, 0, "")

	cvCap := min4(stack.Cap, lang.CV, loc.CV, sen.CV)
	nsCap := min4(5, lang.NS, loc.NS, sen.NS)
	cv, ns := min2(cvRaw, cvCap), min2(nsRaw, nsCap)

	gaps, gapsDropped := verifyGaps(m.SoftGaps, jd)
	strengths, strengthsDropped := fit.VerifyAgainstCv(m.TopStrengths, s.Cfg.CVRaw)

	if sen.Reason != "" {
		gaps = dedupPrepend(gaps, "Seniority cap applied: "+sen.Reason, 5)
	}
	var hardStops []string
	if missingLang != "" {
		hardStops = append(hardStops, "Requires "+missingLang+" fluency — not in CV languages")
	}
	if city != "" {
		hardStops = append(hardStops, "On-site/hybrid in "+city+" — outside the commutable base")
	}

	return &Score{
		Status: state.P1Scored, ID: strconv.Itoa(id), URL: url,
		JDFile:   filepath.Join(s.L.JDs(), strconv.Itoa(id)+".txt"),
		Model:    s.Model,
		ScoredAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Score:    math.Round((float64(cv)*0.625+float64(ns)*0.375)*10) / 10,
		CVMatch:  cv, NorthStar: ns, CVMatchRaw: cvRaw, NorthStarRaw: nsRaw,
		CVCap: cvCap, NSCap: nsCap,
		RedFlagsScore: clampDim(m.RedFlagsScore, 5),
		Company:       orNone(strings.TrimSpace(m.Company), "unknown"),
		Role:          orNone(strings.TrimSpace(m.Role), "unknown"),
		Archetype:     orNone(m.Archetype, "Unknown"),
		HardStops:     nonNil(hardStops),
		SoftGaps:      nonNil(gaps),
		TopStrengths:  nonNil(strengths),
		// Counts, never the text: the dropped items are fabrications by
		// definition, and Phase 2 reads these fields into its own context.
		GapsDropped:      gapsDropped,
		StrengthsDropped: len(strengthsDropped),
		JDSummary:        m.JDSummary,
		Confidence:       orNone(m.Confidence, "Medium"),
	}
}

func (s *Scorer) write(id int, sc *Score) error {
	b, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return err
	}
	return state.WriteAtomic(filepath.Join(s.L.Scores(), strconv.Itoa(id)+".json"), append(b, '\n'))
}

// verifyGaps keeps only gaps whose jd_quote really appears in the JD.
//
// Grounding by construction: the model can copy a span but cannot reliably
// invent one that survives a substring check, so a requirement the JD never
// states is dropped here instead of reaching the report. Matching ignores case
// and whitespace because providers reflow text; a quote under 12 characters is
// rejected outright, being too easy to match by accident.
func verifyGaps(raw []struct {
	Requirement string `json:"requirement"`
	JDQuote     string `json:"jd_quote"`
}, jdText string) ([]string, int) {
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	jd := norm(jdText)
	var gaps []string
	dropped := 0
	for _, g := range raw {
		req := strings.TrimSpace(g.Requirement)
		if req == "" {
			continue
		}
		q := norm(g.JDQuote)
		if len(q) >= 12 && strings.Contains(jd, q) {
			gaps = append(gaps, req)
		} else {
			dropped++
		}
	}
	return gaps, dropped
}

func clampDim(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	if v < 1 {
		return 1
	}
	if v > 5 {
		return 5
	}
	return v
}

func min2(a, b int) int {
	if b < a {
		return b
	}
	return a
}

func min4(a, b, c, d int) int { return min2(min2(a, b), min2(c, d)) }

func orNone(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// dedupPrepend puts head first, drops duplicates, and caps the length.
func dedupPrepend(list []string, head string, cap int) []string {
	out := []string{head}
	seen := map[string]bool{head: true}
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) > cap {
		out = out[:cap]
	}
	return out
}
