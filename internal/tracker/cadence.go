package tracker

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// Cadence is the follow-up schedule, in days.
type Cadence struct {
	AppliedFirst        int `yaml:"applied_first_days"`
	AppliedSubsequent   int `yaml:"applied_subsequent_days"`
	AppliedMaxFollowups int `yaml:"applied_max_followups"`
	RespondedInitial    int `yaml:"responded_initial_days"`
	RespondedSubsequent int `yaml:"responded_subsequent_days"`
	InterviewThankYou   int `yaml:"interview_thankyou_days"`
}

func DefaultCadence() Cadence {
	return Cadence{
		AppliedFirst: 7, AppliedSubsequent: 7, AppliedMaxFollowups: 2,
		RespondedInitial: 1, RespondedSubsequent: 3, InterviewThankYou: 1,
	}
}

// LoadCadence overlays config/profile.yml's followup_cadence on the defaults.
// A missing file, a missing block or a nonsensical value each fall back rather
// than failing: the cadence is a preference, not a contract.
func LoadCadence(profilePath string) Cadence {
	c := DefaultCadence()
	b, err := os.ReadFile(profilePath)
	if err != nil {
		return c
	}
	var doc struct {
		FollowupCadence Cadence `yaml:"followup_cadence"`
	}
	if yaml.Unmarshal(b, &doc) != nil {
		return c
	}
	// Only positive values override; zero means "not set in the file", which is
	// indistinguishable from absent in Go and must not zero a default.
	o := doc.FollowupCadence
	for _, f := range []struct{ src, dst *int }{
		{&o.AppliedFirst, &c.AppliedFirst},
		{&o.AppliedSubsequent, &c.AppliedSubsequent},
		{&o.AppliedMaxFollowups, &c.AppliedMaxFollowups},
		{&o.RespondedInitial, &c.RespondedInitial},
		{&o.RespondedSubsequent, &c.RespondedSubsequent},
		{&o.InterviewThankYou, &c.InterviewThankYou},
	} {
		if *f.src > 0 {
			*f.dst = *f.src
		}
	}
	return c
}

// Urgency ranks how badly a follow-up is wanted.
type Urgency int

const (
	Urgent  Urgency = iota // a response landed today: reply now
	Overdue                // the cadence has elapsed
	Waiting                // on schedule, nothing to do
	Cold                   // out of follow-ups; let it go
)

func (u Urgency) String() string {
	switch u {
	case Urgent:
		return "urgent"
	case Overdue:
		return "overdue"
	case Cold:
		return "cold"
	}
	return "waiting"
}

// Due is one actionable application with its follow-up state worked out.
type Due struct {
	App              Application
	Contacts         []Contact
	FollowupCount    int
	LastFollowup     string
	DaysSinceApplied int
	DaysSinceLast    int // -1 when there has been no follow-up
	NextDate         string
	DaysUntilNext    int
	Urgency          Urgency
}

type Contact struct {
	Email string
	Name  string
}

// actionable is the set of statuses a follow-up can apply to. Everything else
// is either not yet sent or already concluded.
func actionable(status string) bool {
	return status == StatusApplied || status == StatusResponded || status == StatusInterview
}

func (c Cadence) urgency(status string, sinceApp, sinceLast, count int) Urgency {
	switch status {
	case StatusApplied:
		switch {
		case count >= c.AppliedMaxFollowups:
			return Cold
		case count == 0 && sinceApp >= c.AppliedFirst:
			return Overdue
		case count > 0 && sinceLast >= 0 && sinceLast >= c.AppliedSubsequent:
			return Overdue
		}
	case StatusResponded:
		switch {
		case sinceApp < c.RespondedInitial:
			return Urgent
		case sinceApp >= c.RespondedSubsequent:
			return Overdue
		}
	case StatusInterview:
		if sinceApp >= c.InterviewThankYou {
			return Overdue
		}
	}
	return Waiting
}

func (c Cadence) nextDate(status, appDate, lastFollowup string, count int) string {
	add := func(base string, days int) string {
		t, ok := ParseDate(base)
		if !ok {
			return ""
		}
		return t.AddDate(0, 0, days).Format("2006-01-02")
	}
	switch status {
	case StatusApplied:
		if count >= c.AppliedMaxFollowups {
			return "" // cold: there is no next
		}
		if count > 0 && lastFollowup != "" {
			return add(lastFollowup, c.AppliedSubsequent)
		}
		return add(appDate, c.AppliedFirst)
	case StatusResponded:
		if lastFollowup != "" {
			return add(lastFollowup, c.RespondedSubsequent)
		}
		return add(appDate, c.RespondedSubsequent)
	case StatusInterview:
		return add(appDate, c.InterviewThankYou)
	}
	return ""
}

var emailRe = regexp.MustCompile(`[\w.-]+@[\w.-]+\.\w+`)

// The separator has to be consumed. The Node version listed "emailed" with no
// trailing \s, so "Emailed Jane Doe at …" only matched when the name was jammed
// against the verb, and every note written the documented way came back with a
// null name.
var nameRe = regexp.MustCompile(`(?i)(?:emailed\s+|contact[:\s]+|to\s+)([A-Z][a-z]+ ?[A-Z]?[a-z]*)\s*(?:at|@|$)`)

func extractContacts(notes string) []Contact {
	var out []Contact
	for _, email := range emailRe.FindAllString(notes, -1) {
		c := Contact{Email: email}
		before := notes[:strings.Index(notes, email)]
		if m := nameRe.FindStringSubmatch(before); m != nil {
			c.Name = strings.TrimSpace(m[1])
		}
		out = append(out, c)
	}
	return out
}

// DueList works out what needs a follow-up, most urgent first.
func DueList(apps []Application, fus []Followup, c Cadence, now time.Time) []Due {
	byApp := map[int][]Followup{}
	for _, f := range fus {
		byApp[f.AppNum] = append(byApp[f.AppNum], f)
	}
	// Truncate to midnight: "days since" is a count of calendar days, and
	// comparing against a wall-clock now makes it flap over the course of a day.
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	var out []Due
	for _, a := range apps {
		if !actionable(a.Status) {
			continue
		}
		appDate, ok := ParseDate(a.Date)
		if !ok {
			continue
		}
		mine := byApp[a.Num]
		sort.Slice(mine, func(i, j int) bool { return mine[i].Date > mine[j].Date })

		d := Due{
			App:              a,
			Contacts:         extractContacts(a.Notes),
			FollowupCount:    len(mine),
			DaysSinceApplied: daysBetween(appDate, today),
			DaysSinceLast:    -1,
		}
		if len(mine) > 0 {
			d.LastFollowup = mine[0].Date
			if t, ok := ParseDate(d.LastFollowup); ok {
				d.DaysSinceLast = daysBetween(t, today)
			}
		}
		d.Urgency = c.urgency(a.Status, d.DaysSinceApplied, d.DaysSinceLast, d.FollowupCount)
		d.NextDate = c.nextDate(a.Status, a.Date, d.LastFollowup, d.FollowupCount)
		if t, ok := ParseDate(d.NextDate); ok {
			d.DaysUntilNext = daysBetween(today, t)
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Urgency < out[j].Urgency })
	return out
}

// NextFollowupNum is the number a new follow-up row takes.
func NextFollowupNum(fus []Followup) int {
	max := 0
	for _, f := range fus {
		if f.Num > max {
			max = f.Num
		}
	}
	return max + 1
}

// AppendFollowup adds a row to follow-ups.md, creating the file with its header
// when absent.
func AppendFollowup(path string, f Followup) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		b = []byte("# Follow-ups Log\n\n| # | App | Date | Company | Role | Channel | Contact | Notes |\n|---|-----|------|---------|------|---------|---------|-------|\n")
	} else if err != nil {
		return err
	}
	body := strings.TrimRight(string(b), "\n")
	row := strings.Join([]string{
		"", itoa(f.Num), itoa(f.AppNum), f.Date, clean(f.Company), clean(f.Role),
		clean(f.Channel), clean(f.Contact), clean(f.Notes), "",
	}, " | ")
	return os.WriteFile(path, []byte(body+"\n"+strings.TrimSpace(row)+"\n"), 0o644)
}

// RemoveFollowup deletes a row by number — the undo for a mis-keyed nudge.
func RemoveFollowup(path string, num int) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var keep []string
	for _, line := range strings.Split(string(b), "\n") {
		if p := splitRow(line); len(p) >= 8 {
			if n, err := atoiStrict(p[1]); err == nil && n == num {
				continue
			}
		}
		keep = append(keep, line)
	}
	return os.WriteFile(path, []byte(strings.Join(keep, "\n")), 0o644)
}

// clean substitutes a literal pipe, which would corrupt the row and every
// column after it.
func clean(s string) string {
	s = strings.ReplaceAll(s, "|", "/")
	if s == "" {
		return "—"
	}
	return s
}
