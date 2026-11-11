// Package extract reads the facts Phase 1 and 2's caps need out of a posting:
// office attendance, human languages, years demanded and the programming
// languages the role names (docs/PRD-TAILORING-REWORK.md §5.1). The caps in
// internal/fit read these in place of word lists and regexes over the posting.
//
// One schema-constrained call returns every fact with the words of the posting
// that state it, and code keeps a fact only if those words are in the posting
// (the trick phase1.verifyGaps plays on gap quotes: a model can copy a span but
// cannot reliably invent one that survives a substring check). A fact that fails
// is unknown, never guessed, and unknown never fires a cap.
package extract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

// numCtx is fixed so the same prompt is the same request on every run. The
// posting is capped at 10k characters upstream, about 2.5k tokens, so 8192
// would hold it. It is 12288 because that is what p1.score asks for: Ollama
// reloads a model whenever num_ctx changes, and with 8192 here the 4B was
// loaded twice per new posting, 3.7 s each time.
const numCtx = 12288

// Posting is the text the extraction reads and the quotes are checked against:
// the cleaned posting, capped. Every caller must use it, or the cache misses.
func Posting(jd string) string { return text.CleanJD(jd, 10_000) }

// minQuote is the shortest quote that counts. "Lead" and "Sr." are real title
// quotes, so it is lower than phase1's 12; the value-in-quote checks below do
// the work a longer minimum would.
const minQuote = 3

// Attendance values.
const (
	Onsite      = "onsite"
	Hybrid      = "hybrid"
	Remote      = "remote"
	Unspecified = "unspecified"
)

// Requirement values for a language.
const (
	Required  = "required"
	Preferred = "preferred"
)

// Integers are 0 when the posting does not say. The schema carries 0 rather
// than a null: no field here has a meaningful 0 (zero office days is "remote",
// zero years is "none stated"), and a [integer, null] union is a grammar the
// local Ollama is not guaranteed to compile.

// Attendance is whether and where the role needs an office.
type Attendance struct {
	Mode         string `json:"mode"`
	DaysPerWeek  int    `json:"days_per_week"`
	DaysPerMonth int    `json:"days_per_month"`
	Place        string `json:"place"`
	Quote        string `json:"quote"`
	// Claimed is whether the model stated anything. QuoteOK is whether the
	// quote held up, so QuoteOK/Claimed is the rate that matters.
	Claimed bool `json:"claimed"`
	QuoteOK bool `json:"quote_ok"`
}

// Language is one human language the posting asks for.
type Language struct {
	Language    string `json:"language"`
	Requirement string `json:"requirement"`
	Quote       string `json:"quote"`
}

// Years is the experience demanded.
type Years struct {
	MinYears int    `json:"min_years"`
	Quote    string `json:"quote"`
	Claimed  bool   `json:"claimed"`
	QuoteOK  bool   `json:"quote_ok"`
}

// Languages is the vocabulary of StackItem.Language: the programming languages
// a role or a CV can name. It is the model's output vocabulary, not a matcher
// over postings, so a language outside it comes back as None and fires nothing.
// The enum is what makes both sides spell a language the same way.
var Languages = []string{"python", "java", "javascript", "kotlin", "c#", "c++", "c", "go", "rust", "ruby", "php",
	"scala", "elixir", "swift", "dart", "haskell", "clojure", "perl", "lua", "r", None}

// None is the Languages value for a term that is not a programming language.
const None = "none"

// StackItem is one term of the posting's stack that belongs to a programming
// language. Term is what the posting says (Django), Language the programming
// language it belongs to (python).
type StackItem struct {
	Term        string `json:"term"`
	Language    string `json:"language"`
	Requirement string `json:"requirement"`
	Quote       string `json:"quote"`
}

// Facts are the verified facts of one posting.
type Facts struct {
	Attendance Attendance `json:"attendance"`
	// Languages holds only the entries that passed. LanguagesClaimed and
	// LanguagesOK count the entries the model gave and the ones kept.
	Languages        []Language  `json:"languages"`
	LanguagesClaimed int         `json:"languages_claimed"`
	LanguagesOK      int         `json:"languages_ok"`
	Years            Years       `json:"years"`
	Stack            []StackItem `json:"stack"`
}

// Reply is what the schema constrains the model to. Each object lists its quote
// first: the grammar emits properties in schema order, so the value is
// generated after the words it has to agree with.
type Reply struct {
	Attendance struct {
		Quote        string `json:"quote"`
		Mode         string `json:"mode"`
		DaysPerWeek  int    `json:"days_per_week"`
		DaysPerMonth int    `json:"days_per_month"`
		Place        string `json:"place"`
	} `json:"attendance"`
	Languages []struct {
		Quote       string `json:"quote"`
		Language    string `json:"language"`
		Requirement string `json:"requirement"`
	} `json:"languages"`
	Years struct {
		Quote    string `json:"quote"`
		MinYears int    `json:"min_years"`
	} `json:"years"`
	Stack []struct {
		Quote       string `json:"quote"`
		Term        string `json:"term"`
		Language    string `json:"language"`
		Requirement string `json:"requirement"`
	} `json:"stack"`
}

// capped is a string with a length bound. Ollama compiles the schema into a
// decoding grammar; an unbounded string lets a 4B run on to the context limit,
// and a bound above ~1800 stops the grammar compiling (see phase2.capped).
func capped(n int) ollama.Schema {
	return ollama.Obj(ollama.F("type", "string"), ollama.F("maxLength", n))
}

// Schema is the reply's JSON Schema.
func Schema() ollama.Schema {
	return ollama.Object(ollama.Obj(
		ollama.F("attendance", ollama.Object(ollama.Obj(
			ollama.F("quote", capped(300)),
			ollama.F("mode", ollama.Str(Onsite, Hybrid, Remote, Unspecified)),
			ollama.F("days_per_week", ollama.Int(0, 7)),
			ollama.F("days_per_month", ollama.Int(0, 31)),
			ollama.F("place", capped(80)),
		))),
		ollama.F("languages", ollama.Arr(ollama.Object(ollama.Obj(
			ollama.F("quote", capped(300)),
			ollama.F("language", capped(30)),
			ollama.F("requirement", ollama.Str(Required, Preferred)),
		)), 5)),
		ollama.F("years", ollama.Object(ollama.Obj(
			ollama.F("quote", capped(200)),
			ollama.F("min_years", ollama.Int(0, 30)),
		))),
		ollama.F("stack", ollama.Arr(ollama.Object(ollama.Obj(
			ollama.F("quote", capped(200)),
			ollama.F("term", capped(40)),
			ollama.F("language", ollama.Str(Languages...)),
			ollama.F("requirement", ollama.Str(Required, Preferred)),
		)), 8)),
	))
}

var punct = strings.NewReplacer("’", "'", "‘", "'", "“", `"`, "”", `"`, "–", "-", "—", "-", " ", " ")

// Norm is the form quotes and postings are compared in: case-folded, with runs
// of whitespace collapsed and typographic punctuation straightened. Providers
// reflow text and a 4B straightens a curly apostrophe without being asked; both
// sides go through the same fold, so it cannot make a quote match that is not
// there.
func Norm(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(punct.Replace(s)), " "))
}

var numberWords = map[string]string{
	"1": "one", "2": "two", "3": "three", "4": "four", "5": "five", "6": "six",
	"7": "seven", "8": "eight", "9": "nine", "10": "ten", "11": "eleven", "12": "twelve",
}

// hasNumber is whether the quote contains n as digits or as a word. A 4B reads
// "5-7 years" and reports 8; the quote is in the posting, so the substring
// check alone passes it.
func hasNumber(quote string, n int) bool {
	s := fmt.Sprint(n)
	for i := 0; i+len(s) <= len(quote); i++ {
		if quote[i:i+len(s)] != s {
			continue
		}
		before := i == 0 || quote[i-1] < '0' || quote[i-1] > '9'
		after := i+len(s) == len(quote) || quote[i+len(s)] < '0' || quote[i+len(s)] > '9'
		if before && after {
			return true
		}
	}
	w, ok := numberWords[s]
	return ok && strings.Contains(quote, w)
}

var (
	// attendanceCue is what a quote must say before it counts as a statement
	// about where the work is done: a requirement, not a mention of an office
	// ("our office is in Harpenden"). The first full run of the 4B over 392
	// postings read "Location: London, UK" as on-site in 38 of them, which
	// states where the company is and nothing about attendance. The words are
	// the vocabulary of attendance, not of places, so the list does not grow
	// with the next city.
	attendanceCue = regexp.MustCompile(`(?i)\b(on-?site|in[- ]?office|(in|at|to|from) the office|office[- ]based|in[- ]person|hybrid|remote|work(ing)? from home|wfh|days?|full[- ]time|based (in|at|out of)|commut\w*|relocat\w*)\b`)

	// temporaryCue marks an arrangement for a limited period. The prompt says to
	// report only the standing arrangement and the 4B did not follow it ("we
	// expect you to spend most days in our Milan office during your first few
	// months, regardless of your long-term work location"). A quote with one of
	// these and no "then" / "after that" states no standing rule.
	temporaryCue = regexp.MustCompile(`(?i)\b((first|initial)\s+(\w+\s+)?(weeks?|months?|year)|initially|onboarding|probation\w*|long-term work location)\b`)
	thenCue      = regexp.MustCompile(`(?i)\b(then|after that|thereafter|afterwards)\b`)

	// yearsCue keeps "6+ months hands-on with agentic editors" from becoming a
	// six-year demand.
	yearsCue = regexp.MustCompile(`(?i)\b(years?|yrs?)\b`)

	// regionTokens are the words of a place that names a country or a region.
	// The prompt says to leave those out; the 4B did not always.
	regionTokens = map[string]bool{"uk": true, "united": true, "kingdom": true, "us": true, "usa": true, "states": true,
		"america": true, "england": true, "scotland": true, "wales": true, "europe": true, "eu": true, "emea": true,
		"apac": true, "global": true, "worldwide": true, "and": true, "or": true, "remote": true, "ireland": true}
)

// isProgramming is whether a name is a programming language. TypeScript is not
// in Languages because the stack field folds it into javascript, but it still is
// one.
func isProgramming(name string) bool {
	n := strings.ToLower(name)
	return n == "typescript" || slices.Contains(Languages, n)
}

// regionOnly reports a place made of country and region words only.
func regionOnly(place string) bool {
	toks := strings.FieldsFunc(strings.ToLower(place), func(r rune) bool { return !unicode.IsLetter(r) })
	if len(toks) == 0 {
		return false
	}
	for _, t := range toks {
		if !regionTokens[t] {
			return false
		}
	}
	return true
}

// Verify turns the model's reply into facts, keeping a fact only if its quote
// is in the posting. posting must be the text the model was shown.
func Verify(r Reply, posting string) Facts {
	post := Norm(posting)
	found := func(quote string) bool {
		q := Norm(quote)
		return utf8.RuneCountInString(q) >= minQuote && strings.Contains(post, q)
	}
	var f Facts

	a := r.Attendance
	// The 4B writes the schema's own word where it has nothing to say: place
	// "unspecified", which would count as a claim and then fail its quote.
	if strings.EqualFold(strings.TrimSpace(a.Place), Unspecified) {
		a.Place = ""
	}
	f.Attendance = Attendance{Mode: Unspecified, Quote: strings.TrimSpace(a.Quote)}
	f.Attendance.Claimed = (a.Mode != "" && a.Mode != Unspecified) || a.DaysPerWeek > 0 || a.DaysPerMonth > 0 || strings.TrimSpace(a.Place) != ""
	if f.Attendance.Claimed && found(a.Quote) && attendanceCue.MatchString(Norm(a.Quote)) &&
		(!temporaryCue.MatchString(Norm(a.Quote)) || thenCue.MatchString(Norm(a.Quote))) {
		f.Attendance.QuoteOK = true
		if a.Mode == Onsite || a.Mode == Hybrid || a.Mode == Remote {
			f.Attendance.Mode = a.Mode
		}
		f.Attendance.DaysPerWeek, f.Attendance.DaysPerMonth = a.DaysPerWeek, a.DaysPerMonth
		// The place is allowed to sit apart from the quote ("Location: London"
		// in a header, "3 days a week" in the body), so it is held to the
		// posting, not to the quote. Not in the posting at all means invented.
		if p := strings.TrimSpace(a.Place); p != "" && strings.Contains(post, Norm(p)) && !regionOnly(p) {
			f.Attendance.Place = p
		}
	}

	for _, l := range r.Languages {
		f.LanguagesClaimed++
		lang := strings.TrimSpace(l.Language)
		// The quote has to name the language it is evidence for: "fluent in
		// English and German" is no evidence for "French".
		// The prompt leaves programming languages out and the 4B sometimes lists
		// TypeScript or C anyway; the stack field is where those belong.
		if lang != "" && !isProgramming(lang) && found(l.Quote) && strings.Contains(Norm(l.Quote), Norm(lang)) &&
			(l.Requirement == Required || l.Requirement == Preferred) {
			f.LanguagesOK++
			f.Languages = append(f.Languages, Language{Language: lang, Requirement: l.Requirement, Quote: strings.TrimSpace(l.Quote)})
		}
	}

	f.Years = Years{Quote: strings.TrimSpace(r.Years.Quote), Claimed: r.Years.MinYears > 0}
	if f.Years.Claimed && found(r.Years.Quote) && hasNumber(Norm(r.Years.Quote), r.Years.MinYears) && yearsCue.MatchString(r.Years.Quote) {
		f.Years.QuoteOK, f.Years.MinYears = true, r.Years.MinYears
	}

	for _, it := range r.Stack {
		term := strings.TrimSpace(it.Term)
		// The quote has to name the term it is evidence for, as with a human
		// language, and the term has to belong to a language.
		if term != "" && it.Language != None && slices.Contains(Languages, it.Language) &&
			found(it.Quote) && strings.Contains(Norm(it.Quote), Norm(term)) &&
			(it.Requirement == Required || it.Requirement == Preferred) {
			f.Stack = append(f.Stack, StackItem{Term: term, Language: it.Language, Requirement: it.Requirement, Quote: strings.TrimSpace(it.Quote)})
		}
	}
	return f
}

// Extractor makes the call and caches it. OL may be nil, and then it answers
// only from the cache.
type Extractor struct {
	OL     *ollama.Client
	Model  string
	System string
	Calls  int // model calls made, for the cache-hit tests

	mu    sync.Mutex // Phase 1 runs workers; guards cache and Calls
	path  string
	check string // hash of the prompt and the schema
	cache map[string]Reply
	cv    map[string][]string // CVLanguages: prompt+evidence hash -> languages

	// CVSystem is the prompt for CVLanguages and Entities the terms it classifies.
	CVSystem string
	Entities []string
}

type cacheFile struct {
	Model   string              `json:"model"`
	Updated string              `json:"updated"`
	Replies map[string]Reply    `json:"replies"`
	CV      map[string][]string `json:"cv,omitempty"`
}

// CachePath is derived data, so it lives with the other caches.
func CachePath(cacheDir, model string) string {
	return filepath.Join(cacheDir, "extract-"+strings.NewReplacer(":", "_", "/", "_").Replace(model)+".json")
}

func sum(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

// New loads the cache. The cache holds the model's reply, not the verified
// facts, so the check can change without paying for the calls again. The key is
// the model (one file per model), the prompt with the schema, and the posting.
func New(ol *ollama.Client, cacheDir, model, system string) *Extractor {
	if model == "" {
		model = ollama.ModelScreen
	}
	schema, _ := json.Marshal(Schema())
	e := &Extractor{OL: ol, Model: model, System: system, path: CachePath(cacheDir, model),
		check: sum(system, string(schema)), cache: map[string]Reply{}, cv: map[string][]string{}}
	if b, err := os.ReadFile(e.path); err == nil {
		var f cacheFile
		if json.Unmarshal(b, &f) == nil && f.Model == model && f.Replies != nil { // a torn file is a cold cache
			e.cache = f.Replies
			if f.CV != nil {
				e.cv = f.CV
			}
		}
	}
	return e
}

func (e *Extractor) save() error {
	b, err := json.MarshalIndent(cacheFile{Model: e.Model, Updated: time.Now().Format(time.RFC3339), Replies: e.cache, CV: e.cv}, "", " ")
	if err != nil {
		return err
	}
	return state.WriteAtomic(e.path, append(b, '\n'))
}

// Cached is Extract that never calls the model. Phase 2 uses it: a 4B call
// between 30B stages would swap the models in and out of 6 GB, and Phase 1 has
// already read the same posting.
func (e *Extractor) Cached(posting string) (Facts, bool) {
	e.mu.Lock()
	r, ok := e.cache[e.check+":"+sum(posting)]
	e.mu.Unlock()
	if !ok {
		return Facts{}, false
	}
	return Verify(r, posting), true
}

// Extract returns the verified facts of one posting, from the cache or one
// model call. posting is the cleaned text the caps would read.
func (e *Extractor) Extract(ctx context.Context, posting string) (Facts, error) {
	key := e.check + ":" + sum(posting)
	e.mu.Lock()
	r, ok := e.cache[key]
	e.mu.Unlock()
	if ok {
		return Verify(r, posting), nil
	}
	if e.OL == nil {
		return Facts{}, fmt.Errorf("extract: reply not in the cache")
	}
	err := e.OL.Chat(ctx, ollama.Request{
		Model: e.Model, System: e.System,
		User:    "## Job Posting\n" + posting,
		Schema:  Schema(),
		Options: ollama.Options{Temperature: 0, NumCtx: numCtx},
		Timeout: 3 * time.Minute, Label: "p1.extract",
	}, &r)
	if err != nil {
		return Facts{}, err
	}
	e.mu.Lock()
	e.Calls++
	e.cache[key] = r
	err = e.save()
	e.mu.Unlock()
	if err != nil {
		return Facts{}, err
	}
	return Verify(r, posting), nil
}
