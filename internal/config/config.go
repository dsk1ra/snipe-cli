// Package config loads the user layer: cv.md, config/profile.md and
// config/profile.yml.
//
// These are never auto-updated. All personalisation lives here, and
// config/profile.md is a hard runtime dependency of Phases 1 and 2.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

// OutsideTargets is the archetype the model falls back to when nothing fits.
const OutsideTargets = "Outside targets"

type Config struct {
	CV         string // cv.md, cleaned for prompts
	CVRaw      string // cv.md verbatim, for the rules that read its structure
	ProfileMD  string // config/profile.md
	ProfileYML string // config/profile.yml, raw text
	Archetypes []string
	Comp       *text.CompTargets
	Thresholds Thresholds
	Candidate  Candidate
	Goals      Goals
	// PinnedProjects ship whatever the posting scores them. A deliberate
	// override of the relevance ranker, so it belongs in the user layer.
	PinnedProjects []string
}

// Goals are the targets the queue's side panel measures the day against.
//
// Daily scans is the input, daily and weekly applications the output. Three
// numbers rather than one per bucket: the panel is 17 columns wide, and a fourth
// bar costs two lines to say what the other three already do.
type Goals struct {
	DailyScans    int `yaml:"daily_scans"`
	DailyApplied  int `yaml:"daily_applications"`
	WeeklyApplied int `yaml:"weekly_applications"`
}

// DefaultGoals is what the panel measures against until profile.yml says
// otherwise. Taken from this tree's own record, where the busy days run 20–33
// scans and the quiet ones 1–4: ten is a day worth having rather than a day to
// despair at.
var DefaultGoals = Goals{DailyScans: 10, DailyApplied: 5, WeeklyApplied: 25}

// Candidate is the header block of the CV: profile.yml's `candidate:` map.
type Candidate struct {
	FullName string `yaml:"full_name"`
	Email    scalar `yaml:"email"`
	Phone    scalar `yaml:"phone"`
	Location string `yaml:"location"`
	LinkedIn scalar `yaml:"linkedin"`
	GitHub   scalar `yaml:"github"`
}

// scalar takes a YAML scalar of any type as text.
//
// An unquoted `phone: +441234567890` is a YAML integer, and unmarshalling it
// into a string fails — which would abort the whole profile decode, silently
// taking the archetype list and the score threshold with it, since Load ignores
// that error by design.
type scalar string

func (s *scalar) UnmarshalYAML(b []byte) error {
	*s = scalar(strings.Trim(strings.TrimSpace(string(b)), `"'`))
	return nil
}

func (s scalar) String() string { return string(s) }

// Thresholds are the profile's own gates, overriding the built-in defaults.
type Thresholds struct {
	AutoPDFScore float64
}

type profileDoc struct {
	TargetRoles struct {
		Archetypes []archetype `yaml:"archetypes"`
	} `yaml:"target_roles"`
	AutoPDFScoreThreshold float64   `yaml:"auto_pdf_score_threshold"`
	Candidate             Candidate `yaml:"candidate"`
	Goals                 Goals     `yaml:"goals"`
	CV                    struct {
		PinnedProjects []string `yaml:"pinned_projects"`
	} `yaml:"cv"`
}

// archetype is either a bare string or a mapping with a name. Both spellings
// appear in real profiles.
type archetype struct {
	Name string
}

func (a *archetype) UnmarshalYAML(b []byte) error {
	var s string
	if err := yaml.Unmarshal(b, &s); err == nil {
		a.Name = s
		return nil
	}
	var m struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(b, &m); err != nil {
		return err
	}
	a.Name = m.Name
	return nil
}

func Load(l paths.Layout) (*Config, error) {
	raw, err := os.ReadFile(l.CV())
	if err != nil {
		return nil, fmt.Errorf("cv.md is required: %w", err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, fmt.Errorf("cv.md is empty (%s)", l.CV())
	}
	c := &Config{
		CVRaw: strings.TrimSpace(string(raw)),
		CV:    text.CleanCV(strings.TrimSpace(string(raw))),
	}
	c.ProfileMD = readSafe(l.ProfileMD())
	c.ProfileYML = readSafe(l.ProfileYML())
	c.Comp = text.ParseCompTargets(c.ProfileYML)
	c.Thresholds.AutoPDFScore = 3.0
	c.Goals = DefaultGoals

	var doc profileDoc
	if yaml.Unmarshal([]byte(c.ProfileYML), &doc) == nil {
		for _, a := range doc.TargetRoles.Archetypes {
			if a.Name != "" {
				c.Archetypes = append(c.Archetypes, a.Name)
			}
		}
		if doc.AutoPDFScoreThreshold > 0 {
			c.Thresholds.AutoPDFScore = doc.AutoPDFScoreThreshold
		}
		// Per field, so a profile that sets one goal does not zero the other
		// two back to nothing.
		if doc.Goals.DailyScans > 0 {
			c.Goals.DailyScans = doc.Goals.DailyScans
		}
		if doc.Goals.DailyApplied > 0 {
			c.Goals.DailyApplied = doc.Goals.DailyApplied
		}
		if doc.Goals.WeeklyApplied > 0 {
			c.Goals.WeeklyApplied = doc.Goals.WeeklyApplied
		}
		c.Candidate = doc.Candidate
		c.PinnedProjects = doc.CV.PinnedProjects
	}
	return c, nil
}

// ArchetypeEnum constrains the model's archetype field to the profile's own
// list, plus the escape hatch.
//
// As free text this field was the single biggest source of bad scores: 27% of a
// 144-offer corpus came back "Outside targets" — 38 of those 39 with north_star
// 1–2, i.e. straight into the P1 gate — including roles whose title matched a
// primary archetype almost word for word. profile.yml and profile.md may also
// punctuate the same archetype differently, so the model had no single list to
// match against and bailed out. The grammar decides now, and profile.yml is the
// one source.
//
// Returns nil when the profile is missing or unparseable, in which case the
// caller must leave the field as free text rather than constraining it to a
// wrong list.
func (c *Config) ArchetypeEnum() []string {
	if len(c.Archetypes) == 0 {
		return nil
	}
	return append(append([]string{}, c.Archetypes...), OutsideTargets)
}

// Hash fingerprints the CV, for cache keys that must invalidate when it changes.
func (c *Config) Hash() string {
	sum := sha256.Sum256([]byte(c.CVRaw))
	return hex.EncodeToString(sum[:])[:16]
}

func readSafe(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Prompt reads a system prompt, preferring a gitignored personal override.
//
// The shipped *.md is a generic placeholder; *.local.md holds the real metrics.
// Every prompt in the pipeline follows this rule.
//
// The file is returned verbatim. Phase 1's prompt is a whole file on both sides,
// so its trailing newline is part of it; the Phase 2 prompts were extracted from
// array literals that were joined without one, so those callers use PromptLine.
func Prompt(l paths.Layout, name string) (string, error) {
	local := strings.TrimSuffix(name, ".md") + ".local.md"
	if b, err := os.ReadFile(l.Prompt(local)); err == nil {
		return string(b), nil
	}
	b, err := os.ReadFile(l.Prompt(name))
	if err != nil {
		return "", fmt.Errorf("prompt %s not found: %w", name, err)
	}
	return string(b), nil
}

// PromptLines is Prompt without the file's trailing newline.
//
// The Phase 2 prompts live in files here but were built by joining an array of
// lines in the Node implementation, which produces no trailing newline. One
// stray byte changes the prompt, and a changed prompt changes the output — this
// showed up as a one-character length difference in the request dump and
// visibly different prose downstream. Trimming here rather than storing files
// without a final newline keeps them ordinary text files that an editor cannot
// silently "fix".
func PromptLines(l paths.Layout, name string) (string, error) {
	s, err := Prompt(l, name)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(s, "\n"), nil
}

// LoadDotEnv reads KEY=value lines from .env into the process environment.
//
// Only the provider layer needs it, and only for portal scanning, but it runs
// at startup so os.Getenv is the single way anything reads a key. An existing
// environment variable always wins: exporting one for a single run is how you
// override the file, and silently losing that would be surprising.
//
// Deliberately not a dotenv library. This file is one key today and the format
// it needs is KEY=value with optional quotes and # comments. A dependency for
// twenty lines is a dependency to keep updated.
//
// Missing or unreadable is not an error. A checkout with no .env is the normal
// case: without a key the Apify providers sit out and everything else runs.
func LoadDotEnv(path string) {
	f, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(f), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) > 1 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if k == "" || os.Getenv(k) != "" {
			continue
		}
		_ = os.Setenv(k, v)
	}
}
