package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// The framings pass (PRD §6.1): a framing is a phrase employers use that the
// facts of one bullet genuinely evidence ("event-driven microservices" for ten
// services talking only through a Kafka log). The phrases come from past
// postings, never from the model, so an approved framing is always a spelling
// some employer has actually written.

// Phrase is one requirement phrase of the corpus and how many postings used it.
type Phrase struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
}

// EmbedFunc embeds texts in order. Injected so tests need no Ollama.
type EmbedFunc func(ctx context.Context, texts []string) ([][]float64, error)

// FramingOpts are the knobs of one pass.
type FramingOpts struct {
	K        int     // candidates per bullet
	Floor    float64 // cosine below which a phrase is never a candidate
	MinCount int     // postings a phrase must appear in to enter the corpus
	Refresh  bool    // redo bullets already framed, if nothing on them is decided
}

func (o FramingOpts) withDefaults() FramingOpts {
	if o.K <= 0 {
		o.K = 8
	}
	if o.MinCount <= 0 {
		o.MinCount = 2
	}
	return o
}

// FramingStats is what a pass did, per entry where it matters.
type FramingStats struct {
	Phrases      int            // corpus size
	Bullets      int            // bullets sent to the model or checked for candidates
	NoCandidates int            // of those, bullets with nothing above the floor
	Calls        int            // model calls
	Dropped      int            // model answers discarded for a bad number
	Proposed     map[string]int // entry id → framings proposed
}

// ── corpus ───────────────────────────────────────────────────────────────────

// ReadReports returns the text of every report in dir. A missing dir is an
// empty corpus: a fresh install has no past postings to learn phrases from.
func ReadReports(dir string) ([]string, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			return nil, err
		}
		out = append(out, string(b))
	}
	return out, nil
}

var (
	parenRe = regexp.MustCompile(`\([^)]*\)?`)
	// A requirement opens with scaffolding that says "you need this" and not
	// what "this" is. Stripped twice over: "strong experience with" is two layers.
	leadRe = regexp.MustCompile(`(?i)^(?:(?:strong|solid|proven|demonstrated|demonstrable|hands-on|extensive|good|excellent|deep|working|practical|prior|previous|commercial|professional|relevant|some|a|an|the|our)\s+)*` +
		`(?:(?:experience|knowledge|understanding|familiarity|proficiency|expertise|background|skills?|ability to|comfortable with|comfortable|familiar with|experienced in|experienced with|fluency in)\s*` +
		`(?:with|in|of|using|on|building|designing|developing|working with|delivering)?\s+)?`)
	splitRe = regexp.MustCompile(`(?i)[,;:]|\s/\s|\s+(?:and/or|and|or|&|with|including|such as|like)\s+`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// singleWord are capability words that stand alone. A one-word phrase is
// otherwise a tool name or an adjective, and a tool name is an entity form, not
// a framing (PRD §6.1).
var singleWord = map[string]bool{
	"containerization": true, "containerisation": true, "microservices": true,
	"observability": true, "orchestration": true, "virtualization": true,
	"virtualisation": true, "cryptography": true, "encryption": true,
	"authentication": true, "authorization": true, "authorisation": true,
	"scalability": true, "concurrency": true, "multithreading": true,
	"serverless": true, "devops": true, "caching": true, "sharding": true,
	"monitoring": true, "debugging": true, "refactoring": true, "automation": true,
	"networking": true, "messaging": true, "profiling": true,
}

const maxPhraseWords = 6

// A cut at "and" or "with" can leave a stub: "write clean", "7+ years
// designing", "their it systems". These never read as a capability, so they are
// dropped before they take one of a bullet's few candidate slots.
var (
	digitRe   = regexp.MustCompile(`\d|\byears?\b`)
	stubStart = map[string]bool{"write": true, "make": true, "maintain": true, "maintaining": true, "ensure": true, "ensuring": true,
		"their": true, "your": true, "our": true, "we": true, "you": true, "as": true, "be": true, "proactive": true, "own": true,
		"designing": true, "building": true, "developing": true, "delivering": true, "identifying": true, "to": true, "of": true}
	stubEnd = map[string]bool{"a": true, "an": true, "the": true, "to": true, "of": true, "in": true, "for": true, "on": true,
		"at": true, "by": true, "from": true, "clean": true, "reliable": true, "high-quality": true, "using": true}
)

// claimsLevel catches phrases that assert a degree, seniority or eligibility
// rather than name a capability. The first live pass proposed "advanced
// proficiency in java" for a teaching bullet and "managing a team of security
// engineers" for a project lead; no fact generalises to those, and a framing is
// reviewed one at a time, so they are not worth a slot.
var claimsLevel = regexp.MustCompile(`\b(advanced|intermediate|expert\w*|senior|lead|leading|managing|manager|first-class|proficien\w*|fluen\w*|extensive|right to work|visa|clearance|sponsorship)\b`)

func stub(p string) bool {
	w := strings.Fields(p)
	return digitRe.MatchString(p) || claimsLevel.MatchString(p) || stubStart[w[0]] || stubEnd[w[len(w)-1]]
}

// DropUnsafeFraming removes pending framings that stub() would no longer let in
// and pending duplicates that differ only in punctuation ("root-cause analysis"
// and "root cause analysis"), from a ledger framed before the filter existed.
// Decided framings are the user's and stay.
func DropUnsafeFraming(l *Ledger) (dropped int) {
	norm := func(p string) string {
		return strings.Join(strings.Fields(strings.NewReplacer("-", " ", ".", "").Replace(p)), " ")
	}
	for ei := range l.Entries {
		for bi := range l.Entries[ei].Bullets {
			b := &l.Entries[ei].Bullets[bi]
			seen := map[string]bool{}
			var keep []Framing
			for _, f := range b.Framings {
				n := norm(f.Phrase)
				if !f.Approved && !f.Rejected && (stub(strings.ToLower(f.Phrase)) || seen[n]) {
					dropped++
					continue
				}
				seen[n] = true
				keep = append(keep, f)
			}
			b.Framings = keep
		}
	}
	return dropped
}

// phrasesOf cuts one requirement or keyword into lowercase candidate phrases.
func phrasesOf(raw string) []string {
	raw = parenRe.ReplaceAllString(raw, " ")
	raw = strings.ReplaceAll(raw, "**", "")
	var out []string
	for _, p := range splitRe.Split(raw, -1) {
		for range 2 {
			p = leadRe.ReplaceAllString(strings.TrimSpace(p), "")
		}
		p = strings.ToLower(strings.Trim(strings.TrimSpace(spaceRe.ReplaceAllString(p, " ")), ".-–—•*\"'"))
		n := len(strings.Fields(p))
		if (n >= 2 && n <= maxPhraseWords || n == 1 && singleWord[p]) && !stub(p) {
			out = append(out, p)
		}
	}
	return out
}

// reportPhrases are the raw strings a report contributes: the Block B
// requirement column and the stage 1 keyword line that closes every report.
func reportPhrases(report string) []string {
	out := cvselect.ExtractBlockBRequirements(report)
	if _, tail, ok := strings.Cut(report, "\n## Keywords"); ok {
		kw, _, _ := strings.Cut(strings.TrimSpace(tail), "\n#")
		out = append(out, strings.Split(kw, ",")...)
	}
	return out
}

// entityOnly says a phrase is nothing but entity names and joiners. It matches
// the phrase whole, or token by token against the single-token entity forms
// ("Redis and Valkey"). A phrase that merely contains an entity ("kafka event
// streaming") is a capability phrase and stays.
func entityOnly(phrase string, forms, tokens map[string]bool) bool {
	if forms[phrase] {
		return true
	}
	fields := strings.FieldsFunc(phrase, func(r rune) bool { return r == ' ' || r == ',' || r == '/' || r == '&' })
	for _, f := range fields {
		if f != "and" && f != "or" && !tokens[f] {
			return false
		}
	}
	return len(fields) > 0
}

func entitySets(entities map[string][]string) (forms, tokens map[string]bool) {
	forms, tokens = map[string]bool{}, map[string]bool{}
	for c, fs := range entities {
		for _, f := range append([]string{c}, fs...) {
			f = strings.ToLower(f)
			forms[f] = true
			if !strings.ContainsAny(f, " /") {
				tokens[f] = true
			}
		}
	}
	return
}

// CollectPhrases builds the corpus from past reports. Count is the number of
// postings that used the phrase: one employer's quirk is not worth a framing
// and a phrase twenty employers wrote is, hence MinCount.
func CollectPhrases(reports []string, entities map[string][]string, minCount int) []Phrase {
	forms, tokens := entitySets(entities)
	counts := map[string]int{}
	for _, r := range reports {
		seen := map[string]bool{}
		for _, raw := range reportPhrases(r) {
			for _, p := range phrasesOf(raw) {
				if !seen[p] && !entityOnly(p, forms, tokens) {
					seen[p] = true
					counts[p]++
				}
			}
		}
	}
	var out []Phrase
	for p, n := range counts {
		if n >= minCount {
			out = append(out, Phrase{p, n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Text < out[j].Text
	})
	return out
}

// ── embedding cache ──────────────────────────────────────────────────────────

type embedCacheFile struct {
	Model string               `json:"model"`
	Vecs  map[string][]float64 `json:"vecs"`
}

// CachedEmbed wraps fn with a text → vector file. Derived data: deleting it
// costs one re-embed. A different fingerprint discards the lot, because cosine
// across two embedders is meaningless without looking wrong.
func CachedEmbed(path, fingerprint string, fn EmbedFunc) EmbedFunc {
	return func(ctx context.Context, texts []string) ([][]float64, error) {
		c := embedCacheFile{Model: fingerprint, Vecs: map[string][]float64{}}
		if b, err := os.ReadFile(path); err == nil {
			var got embedCacheFile
			if json.Unmarshal(b, &got) == nil && got.Model == fingerprint && got.Vecs != nil {
				c = got
			}
		}
		var miss []string
		seen := map[string]bool{}
		for _, t := range texts {
			if _, ok := c.Vecs[t]; !ok && !seen[t] {
				seen[t] = true
				miss = append(miss, t)
			}
		}
		if len(miss) > 0 {
			vecs, err := fn(ctx, miss)
			if err != nil {
				return nil, err
			}
			if len(vecs) != len(miss) {
				return nil, fmt.Errorf("embedder returned %d vectors for %d texts", len(vecs), len(miss))
			}
			for i, t := range miss {
				c.Vecs[t] = vecs[i]
			}
			b, err := json.Marshal(c)
			if err != nil {
				return nil, err
			}
			if err := state.WriteAtomic(path, b); err != nil {
				return nil, err
			}
		}
		out := make([][]float64, len(texts))
		for i, t := range texts {
			out[i] = c.Vecs[t]
		}
		return out, nil
	}
}

// ── candidates ───────────────────────────────────────────────────────────────

// Candidate is a corpus phrase offered to the model for one bullet.
type Candidate struct {
	Phrase string
	Sim    float64
}

// entityGuard rejects a phrase that names a ledger entity the bullet does not
// mention ("gitlab ci/cd" for a bullet about Jenkins). The prompt says the same
// to the model; this is the check that does not depend on it obeying. It errs
// toward dropping: a lost candidate costs a proposal, a kept one costs trust.
type entityGuard []struct {
	re   *regexp.Regexp
	form string
}

func newEntityGuard(entities map[string][]string) entityGuard {
	var g entityGuard
	for c, fs := range entities {
		for _, f := range append([]string{c}, fs...) {
			if len(f) < 3 { // "CI", "Go": too short to match on without false drops
				continue
			}
			g = append(g, struct {
				re   *regexp.Regexp
				form string
			}{regexp.MustCompile(`(?i)(?:^|[^a-z0-9])` + regexp.QuoteMeta(f) + `(?:$|[^a-z0-9])`), strings.ToLower(f)})
		}
	}
	return g
}

func (g entityGuard) allows(phrase, bulletText string) bool {
	bt := strings.ToLower(bulletText)
	for _, e := range g {
		if e.re.MatchString(phrase) && !strings.Contains(bt, e.form) {
			return false
		}
	}
	return true
}

// topCandidates ranks the corpus against one bullet vector.
func topCandidates(bv []float64, ps []Phrase, pv [][]float64, o FramingOpts, ok func(string) bool) ([]Candidate, error) {
	var all []Candidate
	for i, p := range ps {
		if !ok(p.Text) {
			continue
		}
		s, err := embed.Cosine(bv, pv[i])
		if err != nil {
			return nil, err
		}
		if s >= o.Floor {
			all = append(all, Candidate{p.Text, s})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Sim > all[j].Sim })
	if len(all) > o.K {
		all = all[:o.K]
	}
	return all, nil
}

// ── selection ────────────────────────────────────────────────────────────────

func decided(b Bullet) bool {
	for _, f := range b.Framings {
		if f.Approved || f.Rejected {
			return true
		}
	}
	return false
}

// needsFraming is the one rule for which bullets a pass touches. The hash is
// the bullet's identity, so a bullet that still carries FramedAt has been
// through this pass on this exact text. Refresh redoes those, but never a
// bullet where the user has decided something: their decisions hang on it.
func needsFraming(b Bullet, refresh bool) bool {
	if b.Hash == InferredHash || len(liveFacts(b)) == 0 {
		return false
	}
	if b.FramedAt == "" {
		return true
	}
	return refresh && !decided(b)
}

// liveFacts are the facts a framing may rest on: a rejected fact is one the
// user said is not true of them.
func liveFacts(b Bullet) []Fact {
	var out []Fact
	for _, f := range b.Facts {
		if f.Status != "rejected" {
			out = append(out, f)
		}
	}
	return out
}

// FrameDiff says how many bullets a pass would look at and, at most, how many
// model calls that is. It embeds nothing, so it cannot know which bullets will
// have no candidate above the floor.
func FrameDiff(l *Ledger, refresh bool) (bullets, calls int) {
	for _, e := range l.Entries {
		n := 0
		for _, b := range e.Bullets {
			if needsFraming(b, refresh) {
				n++
			}
		}
		bullets += n
		calls += (n + chunk - 1) / chunk
	}
	return
}

// ── the pass ─────────────────────────────────────────────────────────────────

// Frame proposes framings for every bullet that needs them and writes them into
// l, unapproved. Existing framings are never touched: an approved one stays, a
// rejected one is never offered again, and on --refresh only undecided
// proposals on a bullet with no decisions are replaced. l is consistent after
// every chunk, so on an error the caller can still save what was done.
//
// system is prompts/p3-framings.md; now stamps FramedAt.
func Frame(ctx context.Context, ol *ollama.Client, system string, l *Ledger, corpus []Phrase, emb EmbedFunc, o FramingOpts, now string) (FramingStats, error) {
	o = o.withDefaults()
	st := FramingStats{Phrases: len(corpus), Proposed: map[string]int{}}
	if bullets, _ := FrameDiff(l, o.Refresh); bullets == 0 {
		return st, nil
	}
	pv, err := emb(ctx, phraseTexts(corpus))
	if err != nil {
		return st, fmt.Errorf("embedding the phrase corpus: %w", err)
	}
	guard := newEntityGuard(l.Entities)

	for ei := range l.Entries {
		e := &l.Entries[ei]
		var idx []int
		var texts []string
		for bi, b := range e.Bullets {
			if needsFraming(b, o.Refresh) {
				idx = append(idx, bi)
				texts = append(texts, factsText(b))
			}
		}
		if len(idx) == 0 {
			continue
		}
		bv, err := emb(ctx, texts)
		if err != nil {
			return st, fmt.Errorf("%s: embedding bullets: %w", e.ID, err)
		}

		type job struct {
			bi    int
			facts []Fact
			cands []Candidate
		}
		var jobs []job
		for j, bi := range idx {
			b := &e.Bullets[bi]
			st.Bullets++
			if o.Refresh && b.FramedAt != "" { // undecided, by needsFraming: they are about to be replaced
				b.Framings = nil
			}
			have := map[string]bool{}
			for _, f := range b.Framings {
				have[strings.ToLower(f.Phrase)] = true
			}
			cands, err := topCandidates(bv[j], corpus, pv, o, func(p string) bool {
				return !have[p] && guard.allows(p, b.Text)
			})
			if err != nil {
				return st, err
			}
			if len(cands) == 0 {
				b.FramedAt = now
				st.NoCandidates++
				continue
			}
			jobs = append(jobs, job{bi, liveFacts(*b), cands})
		}

		for i := 0; i < len(jobs); i += chunk {
			part := jobs[i:min(i+chunk, len(jobs))]
			in := make([]framingInput, len(part))
			for k, j := range part {
				in[k] = framingInput{j.facts, j.cands}
			}
			ans, dropped, err := frameChunk(ctx, ol, system, *e, in)
			st.Calls++
			if err != nil {
				return st, fmt.Errorf("%s: %w", e.ID, err)
			}
			st.Dropped += dropped
			for k, j := range part {
				b := &e.Bullets[j.bi]
				b.Framings = append(b.Framings, ans[k]...)
				b.FramedAt = now
				st.Proposed[e.ID] += len(ans[k])
			}
		}
	}
	return st, nil
}

func phraseTexts(ps []Phrase) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Text
	}
	return out
}

func factsText(b Bullet) string {
	var t []string
	for _, f := range liveFacts(b) {
		t = append(t, f.Text)
	}
	return strings.Join(t, "; ")
}

type framingInput struct {
	facts []Fact
	cands []Candidate
}

func framingSchema() ollama.Schema {
	one := ollama.Object(ollama.Obj(
		ollama.F("c", ollama.Int(1, 20)),
		ollama.F("supported", ollama.Obj(ollama.F("type", "boolean"))),
		ollama.F("from", ollama.Arr(ollama.Int(1, 30), 8)),
	))
	bullet := ollama.Object(ollama.Obj(
		ollama.F("n", ollama.Int(1, 40)),
		ollama.F("phrases", ollama.Arr(one, 20)),
	))
	return ollama.Object(ollama.Obj(ollama.F("bullets", ollama.Arr(bullet, 40))))
}

// frameChunk is one call for up to `chunk` bullets of one entry. The model
// answers by number: candidate number and fact numbers. The phrase and the fact
// ids are looked up here, so nothing the model retypes can reach the ledger,
// and a number outside the list it was shown drops the whole answer.
func frameChunk(ctx context.Context, ol *ollama.Client, system string, e Entry, in []framingInput) ([][]Framing, int, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Entry: %s\n", e.Title)
	if len(e.Qualifiers) > 0 {
		fmt.Fprintf(&b, "Qualifiers: %s\n", strings.Join(e.Qualifiers, ", "))
	}
	for i, x := range in {
		fmt.Fprintf(&b, "\nBullet %d\nFacts\n", i+1)
		for j, f := range x.facts {
			fmt.Fprintf(&b, "%d. %s\n", j+1, f.Text)
		}
		b.WriteString("Phrases\n")
		for j, c := range x.cands {
			fmt.Fprintf(&b, "%d. %s\n", j+1, c.Phrase)
		}
	}
	var reply struct {
		Bullets []struct {
			N       int `json:"n"`
			Phrases []struct {
				C         int   `json:"c"`
				Supported bool  `json:"supported"`
				From      []int `json:"from"`
			} `json:"phrases"`
		} `json:"bullets"`
	}
	if err := ol.Chat(ctx, ollama.Request{
		Model: ollama.ModelEval, System: system, User: b.String(), Schema: framingSchema(),
		Options: ollama.Options{Temperature: 0, NumCtx: 8192, NumPredict: 3000},
		Timeout: 300 * time.Second, Label: "p3.framings",
	}, &reply); err != nil {
		return nil, 0, err
	}
	out := make([][]Framing, len(in))
	dropped := 0
	done := make([]map[int]bool, len(in))
	for _, rb := range reply.Bullets {
		if rb.N < 1 || rb.N > len(in) {
			dropped += len(rb.Phrases)
			continue
		}
		x := in[rb.N-1]
		if done[rb.N-1] == nil {
			done[rb.N-1] = map[int]bool{}
		}
		for _, p := range rb.Phrases {
			if !p.Supported {
				continue
			}
			if p.C < 1 || p.C > len(x.cands) || done[rb.N-1][p.C] {
				dropped++
				continue
			}
			ids, ok := factIDs(x.facts, p.From)
			if !ok {
				dropped++
				continue
			}
			done[rb.N-1][p.C] = true
			out[rb.N-1] = append(out[rb.N-1], Framing{Phrase: x.cands[p.C-1].Phrase, From: ids})
		}
	}
	return out, dropped, nil
}

// factIDs maps the model's fact numbers to ids, in fact order and without
// repeats. One number outside the list fails the answer: a framing resting
// partly on a fact that does not exist is not a framing of the facts shown.
func factIDs(facts []Fact, nums []int) ([]string, bool) {
	if len(nums) == 0 {
		return nil, false
	}
	want := map[int]bool{}
	for _, n := range nums {
		if n < 1 || n > len(facts) {
			return nil, false
		}
		want[n] = true
	}
	var ids []string
	for i, f := range facts {
		if want[i+1] {
			ids = append(ids, f.ID)
		}
	}
	return ids, true
}
