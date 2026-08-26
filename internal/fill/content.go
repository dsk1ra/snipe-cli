package fill

import (
	"encoding/json"
	"strings"
)

// Content is the model's tailored output. Two fields carry two shapes each: the
// current `projects`/`skills` (objects with curated text) and the legacy
// `selected_projects`/`selected_skills` (bare name strings). Both are still read
// because reports on disk carry both, and a re-render of an old offer must
// produce the same PDF it did the first time.
type Content struct {
	Summary          string          `json:"summary"`
	Experience       []ContentExp    `json:"experience"`
	Projects         json.RawMessage `json:"projects"`
	SelectedProjects json.RawMessage `json:"selected_projects"`
	EducationModules []string        `json:"education_modules"`
	Skills           json.RawMessage `json:"skills"`
	SelectedSkills   []string        `json:"selected_skills"`
}

type ContentExp struct {
	Company string   `json:"company"`
	Bullets []string `json:"bullets"`
}

// ProjectPick is one entry of the model's `projects` field.
type ProjectPick struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Bullets     []string `json:"bullets"`
}

type skillPick struct {
	Category string `json:"category"`
	Items    string `json:"items"`
}

// truthy mirrors `content.projects || content.selected_projects`. An EMPTY array
// is truthy in JavaScript and takes the branch; a missing key or a null does
// not. The two are different instructions — "the model selected nothing", which
// renders the CV's own first four projects, against "this file predates the
// field", which falls back to the legacy one.
func truthy(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "false", "0", `""`:
		return false
	}
	return true
}

// pick decodes an array whose elements are either a bare string or an object.
// A value that is not an array yields nothing, as Array.isArray does.
func pick[T any](raw json.RawMessage, fromString func(string) T) []T {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		var s string
		if json.Unmarshal(it, &s) == nil {
			out = append(out, fromString(s))
			continue
		}
		var v T
		if json.Unmarshal(it, &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

func (c Content) projects() []ProjectPick {
	raw := c.Projects
	if !truthy(raw) {
		raw = c.SelectedProjects
	}
	return pick(raw, func(s string) ProjectPick { return ProjectPick{Name: s} })
}

// ProjectPicks decodes the modern `projects` field only, reporting whether it
// was there. The density ladder trims this list and nothing else: an offer whose
// content predates the field renders its legacy `selected_projects` in full, at
// every step, exactly as the Node does.
func (c Content) ProjectPicks() ([]ProjectPick, bool) {
	if !truthy(c.Projects) {
		return nil, false
	}
	return pick(c.Projects, func(s string) ProjectPick { return ProjectPick{Name: s} }), true
}

// WithProjects returns a copy whose `projects` field is p.
func (c Content) WithProjects(p []ProjectPick) Content {
	b, err := json.Marshal(p)
	if err != nil {
		return c
	}
	c.Projects = b
	return c
}

// skills reports whether the field is an ARRAY, not whether it is truthy: an
// empty `skills: []` still suppresses the legacy `selected_skills`.
func (c Content) skills() ([]skillPick, bool) {
	var items []json.RawMessage
	if json.Unmarshal(c.Skills, &items) != nil {
		return nil, false
	}
	return pick(c.Skills, func(s string) skillPick { return skillPick{Category: s} }), true
}

func ParseContent(b []byte) (Content, error) {
	var c Content
	err := json.Unmarshal(b, &c)
	return c, err
}

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
