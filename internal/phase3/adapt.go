package phase3

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/adapt"
	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/minicheck"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/verify"
)

// --writer adapt: docs/PRD-TAILORING-REWORK.md §6. The selected bullets are
// planned from the fact ledger, rewritten by the 30B, verified, and the best
// verified draft (or the original) goes into the content the template is
// filled from. Anything that goes wrong before that point ships the verbatim
// content instead: adaptation is an upgrade of an offer, never a way to lose
// one.

const (
	WriterVerbatim = "verbatim"
	WriterAdapt    = "adapt"
)

// ParseWriter validates a --writer value; "" is the default, verbatim.
func ParseWriter(s string) (string, error) {
	switch s {
	case "", WriterVerbatim:
		return WriterVerbatim, nil
	case WriterAdapt:
		return WriterAdapt, nil
	}
	return "", fmt.Errorf("unknown writer %q (want %s or %s)", s, WriterVerbatim, WriterAdapt)
}

// AdaptJob is one offer's adaptation.
type AdaptJob struct {
	ID      int // state row id, for the timing log
	Report  string
	Score   float64 // Phase 2's score
	JD      string
	Rows    []phase2.Row
	Terms   []adapt.Term
	Content fill.Content // what verbatim would render
	// Queue appends the offer's inference proposals to the ledger as pending
	// facts. The gate leaves it off so a measurement does not edit the queue.
	Queue bool
	// Stats, when set, receives the rewrite call's counts (the gate's
	// shape_rejects). It stays untouched when the stage came from a checkpoint.
	Stats *adapt.Stats
}

// maxCheckerErrors is how many MiniCheck failures in a row end verification.
// An unanswered question is never a pass, so a dead model would otherwise cost
// a timeout per draft to learn nothing.
const maxCheckerErrors = 3

// Adapt returns the content to render and the truth.json to write beside it.
// A nil truth means the content is the verbatim input, with the reason already
// printed. The only error is the context's.
func (t *Tailor) Adapt(ctx context.Context, ck *state.Checkpoint, j AdaptJob) (fill.Content, *adapt.TruthFile, error) {
	c, tf, err := t.adapt(ctx, ck, j)
	if err == nil {
		return c, tf, nil
	}
	if ctx.Err() != nil {
		return j.Content, nil, ctx.Err()
	}
	fmt.Printf("  #%d: --writer adapt skipped, shipping verbatim: %v\n", j.ID, err)
	return j.Content, nil, nil
}

func (t *Tailor) adapt(ctx context.Context, ck *state.Checkpoint, j AdaptJob) (fill.Content, *adapt.TruthFile, error) {
	led, err := ledger.Load(t.L.CVLedger())
	if err != nil {
		return j.Content, nil, err
	}
	switch {
	case len(led.Entries) == 0:
		return j.Content, nil, fmt.Errorf("no fact ledger; run: snipe ledger")
	case led.CVHash != ledger.CVHash(t.cvForLedger(), embed.ReadFileOr(t.L.Digest())):
		return j.Content, nil, fmt.Errorf("the fact ledger is older than cv.md; run: snipe ledger")
	}
	models, err := t.OL.Up(ctx)
	if err != nil {
		return j.Content, nil, err
	}
	if !slices.ContainsFunc(models, func(m string) bool {
		return m == minicheck.Model || strings.HasPrefix(m, minicheck.Model+":")
	}) {
		return j.Content, nil, fmt.Errorf("model %q is not in Ollama, so no draft can be verified; run: ollama pull %s", minicheck.Model, minicheck.Model)
	}
	system, err := os.ReadFile(t.L.Prompt("p3-rewrite.md"))
	if err != nil {
		return j.Content, nil, err
	}
	chk := t.Checker
	if chk == nil {
		chk = &minicheck.Client{OL: t.OL}
	}

	post := adapt.Posting{
		ID: j.Report, Date: time.Now().Format("2006-01-02"), Score: j.Score,
		InferMinScore: t.Cfg.Thresholds.InferMinScore, Rows: j.Rows, Terms: j.Terms,
	}
	bullets := contentBullets(j.Content)
	plans := adapt.PlanAll(led, bullets, post)

	// Everything the drafts depend on is in the key. The ledger counts without
	// its pending inferences: queueing them below must not make the next resume
	// look like a different ledger.
	postKey, _ := json.Marshal(struct {
		Score float64
		Infer float64
		Rows  []phase2.Row
		Terms []adapt.Term
	}{j.Score, post.InferMinScore, j.Rows, j.Terms})
	in := adaptInput{
		Bullets: bullets, Posting: sha(string(postKey)), Ledger: ledgerKey(led),
		Prompt: sha(string(system) + "\x00" + minicheck.Model), Writer: WriterAdapt,
	}
	draft := func(ctx context.Context, _ adaptInput) ([][]adapt.Verified, error) {
		return t.draftAndVerify(ctx, j, led, string(system), plans, chk)
	}
	var survivors [][]adapt.Verified
	if ck == nil {
		survivors, err = draft(ctx, in)
	} else {
		survivors, err = state.Staged(ctx, ck, "p3.adapt", in, draft)
	}
	if err != nil {
		return j.Content, nil, err
	}

	choices := adapt.SelectAll(led, plans, survivors)
	if os.Getenv("SNIPE_ADAPT_DEBUG") != "" {
		for i, c := range choices {
			if len(survivors[i]) == 0 {
				continue
			}
			fmt.Printf("  #%d: debug source: %s\n", j.ID, plans[i].Bullet.Text)
			for _, v := range survivors[i] {
				fmt.Printf("  #%d: debug   %s (%s): %s\n", j.ID, v.Draft.Kind, v.Class, v.Draft.Text)
			}
			fmt.Printf("  #%d: debug   chose %s: %s\n", j.ID, c.Source, c.Why)
		}
	}
	for i, c := range choices {
		// MaxLines already bounds a draft; this is the assertion that it did.
		if c.Source != "original" && cvselect.BulletLines(c.Text) > cvselect.BulletLines(c.Original) {
			fmt.Printf("  #%d: adapt: %s draft grew a line, reverted\n", j.ID, c.Source)
			choices[i] = keepOriginal(plans[i], c)
		}
	}
	choices, killed := t.killRule(plans, choices)

	repl := map[string]string{}
	changed := 0
	for _, c := range choices {
		if c.Source != "original" {
			repl[c.Original] = c.Text
			changed++
		}
	}
	tf := adapt.NewTruthFile(j.Report, choices)

	queued := 0
	if j.Queue {
		if queued, err = t.queueInferences(adapt.Candidates(led, plans, post)); err != nil {
			fmt.Printf("  #%d: adapt: could not queue inferences: %v\n", j.ID, err)
		}
	}
	framed := 0
	if j.Queue {
		if framed, err = t.queueFramings(adapt.FramingCandidates(led, plans, post)); err != nil {
			fmt.Printf("  #%d: adapt: could not queue framings: %v\n", j.ID, err)
		}
	}
	fmt.Printf("  #%d: adapt: %d of %d line(s) adapted, %d reverted by the kill rule, %d inference(s) and %d framing(s) queued\n",
		j.ID, changed, len(bullets), killed, queued, framed)
	return swapBullets(j.Content, repl), &tf, nil
}

// cvForLedger is cv.md as `snipe ledger` hashed it, byte for byte. CVRaw is
// trimmed, so a file ending in a newline never matched the ledger built from it
// and every offer shipped verbatim with "older than cv.md".
func (t *Tailor) cvForLedger() string {
	if raw, err := os.ReadFile(t.L.CV()); err == nil {
		return string(raw)
	}
	return t.Cfg.CVRaw
}

type adaptInput struct {
	Bullets []string `json:"bullets"`
	Posting string   `json:"posting"`
	Ledger  string   `json:"ledger"`
	Prompt  string   `json:"prompt"`
	Writer  string   `json:"writer"`
}

// draftAndVerify is the stage's body: one rewrite call set on the 30B, then
// every draft through verify.Verify. A draft that was not verified is not
// returned. out[i] belongs to plans[i].
func (t *Tailor) draftAndVerify(ctx context.Context, j AdaptJob, led *ledger.Ledger, system string, plans []adapt.Plan, chk verify.Checker) ([][]adapt.Verified, error) {
	drafts, st, err := adapt.Rewrite(ctx, t.OL, system, plans, led, j.JD)
	if err != nil {
		return nil, err
	}
	if j.Stats != nil {
		*j.Stats = st
	}
	fmt.Printf("  #%d: rewrite: %d draft(s) from %d call(s), %d kept by the local filter, rejects %v\n",
		j.ID, st.Received, st.Calls, st.Kept, st.Rejects)

	start := time.Now()
	defer func() { t.OL.Timing.Wall("p3.verify", j.ID, time.Since(start)) }()
	out := make([][]adapt.Verified, len(plans))
	dropped, failures := 0, 0
	for i, p := range plans {
		ei := slices.IndexFunc(led.Entries, func(e ledger.Entry) bool { return e.ID == p.EntryID })
		if ei < 0 {
			continue
		}
		for _, d := range drafts {
			if d.Hash != p.Bullet.Hash {
				continue
			}
			res, err := verify.Verify(ctx, chk, led.Entries[ei], p.Bullet, d.Text, p.AllowedTerms, verify.Options{Entities: led.Entities})
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if failures++; failures >= maxCheckerErrors {
					return nil, fmt.Errorf("verification: %w", err)
				}
			} else {
				failures = 0
			}
			if res.Class == verify.Supported || res.Class == verify.Inferred {
				out[i] = append(out[i], adapt.Verified{Draft: d, Class: res.Class})
			} else {
				dropped++
				if os.Getenv("SNIPE_ADAPT_DEBUG") != "" {
					fmt.Printf("  #%d: debug   dropped %s: %s (%s)\n", j.ID, d.Kind, d.Text, res.Reason)
				}
			}
		}
	}
	fmt.Printf("  #%d: verify: %d draft(s) dropped, %.1fs\n", j.ID, dropped, time.Since(start).Seconds())
	return out, nil
}

// killRule is PRD §1.1's last line: no offer ships a role whose claims fail
// adapt.Check. Each role is judged on its own; the lines that carry its
// inferred or unapproved claims go back to the original, and if the role still
// fails, every changed line of it does. Select keeps to the budget already, so
// this fires only when the accounting and the selection disagree.
func (t *Tailor) killRule(plans []adapt.Plan, ch []adapt.Choice) ([]adapt.Choice, int) {
	check := t.check
	if check == nil {
		check = adapt.Check
	}
	killed := 0
	for pass := 0; ; pass++ {
		bad := map[string]bool{}
		for _, e := range adapt.Account(ch) {
			if check([]adapt.EntryTruth{e}) != nil {
				bad[e.Entry] = true
			}
		}
		if len(bad) == 0 {
			return ch, killed
		}
		for i, c := range ch {
			if c.Source == "original" || (pass < 2 && !bad[c.Entry]) {
				continue
			}
			if pass == 0 && c.Claims.Inferred == 0 && c.Claims.Unapproved == 0 {
				continue
			}
			ch[i] = keepOriginal(plans[i], c)
			killed++
		}
		if pass >= 2 { // everything is original now; a check that still fails is not ours to fix
			return ch, killed
		}
	}
}

// keepOriginal is the choice for a line that ships as cv.md wrote it.
func keepOriginal(p adapt.Plan, c adapt.Choice) adapt.Choice {
	return adapt.Select(p, c.Original, nil, adapt.Tally{})
}

// queueInferences files the offer's proposed inferences in the ledger as
// pending facts, for the TUI to ask about. A pending fact is neither a claim
// nor a refusal: no plan reads it and no draft can use it, so none reaches a
// PDF before it is approved. The ledger is reloaded under the lock because the
// offer's workers, and the person in the TUI, write the same file.
func (t *Tailor) queueInferences(cs []adapt.Candidate) (int, error) {
	if len(cs) == 0 {
		return 0, nil
	}
	t.ledgerMu.Lock()
	defer t.ledgerMu.Unlock()
	led, err := ledger.Load(t.L.CVLedger())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range cs {
		ei := slices.IndexFunc(led.Entries, func(e ledger.Entry) bool { return e.ID == c.EntryID })
		if ei < 0 {
			continue
		}
		e := &led.Entries[ei]
		next, hold := 0, -1
		known := false
		for bi, b := range e.Bullets {
			if b.Hash == ledger.InferredHash {
				hold = bi
			}
			for _, f := range b.Facts {
				next = max(next, factNum(f.ID))
				// Any status: an approved one is already a claim and a rejected
				// one is a refusal that persists.
				known = known || (f.Source == "inferred" && slices.ContainsFunc(f.Entities, func(x string) bool {
					return strings.EqualFold(x, c.Fact.Entities[0])
				}))
			}
		}
		if known {
			continue
		}
		if hold < 0 {
			e.Bullets = append(e.Bullets, ledger.Bullet{Hash: ledger.InferredHash, Render: new(bool)})
			hold = len(e.Bullets) - 1
		}
		f := c.Fact
		f.ID = e.ID + "/" + strconv.Itoa(next+1) + "a"
		e.Bullets[hold].Facts = append(e.Bullets[hold].Facts, f)
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, ledger.Save(t.L.CVLedger(), led)
}

// queueFramings files the offer's framing proposals on their bullets, undecided.
// An undecided framing is neither allowed in a draft nor a refusal, the way a
// pending inference is not, so none reaches a PDF before the user approves it
// on the ledger screen.
func (t *Tailor) queueFramings(cs []adapt.FramingCandidate) (int, error) {
	if len(cs) == 0 {
		return 0, nil
	}
	t.ledgerMu.Lock()
	defer t.ledgerMu.Unlock()
	led, err := ledger.Load(t.L.CVLedger())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range cs {
		for ei := range led.Entries {
			for bi := range led.Entries[ei].Bullets {
				b := &led.Entries[ei].Bullets[bi]
				if led.Entries[ei].ID != c.EntryID || b.Hash != c.BulletHash ||
					slices.ContainsFunc(b.Framings, func(f ledger.Framing) bool { return strings.EqualFold(f.Phrase, c.Framing.Phrase) }) {
					continue
				}
				b.Framings = append(b.Framings, c.Framing)
				n++
			}
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, ledger.Save(t.L.CVLedger(), led)
}

// factNum is the bullet number in "<entry>/<n><letter>".
func factNum(id string) int {
	s := id[strings.LastIndex(id, "/")+1:]
	n, _ := strconv.Atoi(strings.TrimRight(s, "abcdefghijklmnopqrstuvwxyz"))
	return n
}

// saveTruth leaves dir holding the truth of the PDF just built, or none. A
// truth.json from an earlier adapted run would describe lines this one did not
// ship.
func saveTruth(dir string, tf *adapt.TruthFile) error {
	p := filepath.Join(dir, adapt.TruthName)
	if tf == nil {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return adapt.WriteTruth(p, *tf)
}

// ledgerKey identifies the ledger as the plans see it: without the proposals
// still waiting for a decision.
func ledgerKey(l *ledger.Ledger) string {
	c := *l
	c.Entries = nil
	for _, e := range l.Entries {
		var bs []ledger.Bullet
		for _, b := range e.Bullets {
			b.Facts = slices.DeleteFunc(slices.Clone(b.Facts), func(f ledger.Fact) bool {
				return f.Source == "inferred" && f.Status == ""
			})
			// An undecided framing is no more a claim than a pending inference.
			b.Framings = slices.DeleteFunc(slices.Clone(b.Framings), func(f ledger.Framing) bool {
				return !f.Approved && !f.Rejected
			})
			if b.Hash != ledger.InferredHash || len(b.Facts) > 0 {
				bs = append(bs, b)
			}
		}
		e.Bullets = bs
		c.Entries = append(c.Entries, e)
	}
	b, _ := json.Marshal(c)
	return sha(string(b))
}

func sha(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

// ContentBullets is contentBullets for the gate, which measures the selection.
func ContentBullets(c fill.Content) []string { return contentBullets(c) }

// contentBullets is every bullet the content would print, experience first,
// each once.
func contentBullets(c fill.Content) []string {
	var out []string
	add := func(bs []string) {
		for _, b := range bs {
			if !slices.Contains(out, b) {
				out = append(out, b)
			}
		}
	}
	for _, e := range c.Experience {
		add(e.Bullets)
	}
	if picks, ok := c.ProjectPicks(); ok {
		for _, p := range picks {
			add(p.Bullets)
		}
	}
	return out
}

// swapBullets replaces bullets by exact text, in experience and in projects.
func swapBullets(c fill.Content, repl map[string]string) fill.Content {
	swap := func(bs []string) []string {
		out := slices.Clone(bs)
		for i, b := range out {
			if r, ok := repl[b]; ok {
				out[i] = r
			}
		}
		return out
	}
	exp := make([]fill.ContentExp, len(c.Experience))
	for i, e := range c.Experience {
		exp[i] = fill.ContentExp{Company: e.Company, Bullets: swap(e.Bullets)}
	}
	c.Experience = exp
	if picks, ok := c.ProjectPicks(); ok {
		for i := range picks {
			picks[i].Bullets = swap(picks[i].Bullets)
		}
		c = c.WithProjects(picks)
	}
	return c
}

// PostingTerms is stage 1's term list for an offer: from the checkpoint when
// Phase 2 left it there, else from the report's Keywords line.
//
// ponytail: the report fallback loses the tech_stack/keywords split, so its
// terms are untyped and are never proposed as inferences. Typed parse (PRD
// §5.2) makes this moot.
func PostingTerms(ck *state.Checkpoint, report string) []adapt.Term {
	var p phase2.Parsed
	if ck != nil && ck.Stage("p2.jdparse", &p) {
		return adapt.TermsFromParsed(p)
	}
	var out []adapt.Term
	if m := keywordsRe.FindStringSubmatch(report); m != nil {
		for _, k := range strings.Split(m[1], ",") {
			if k = strings.TrimSpace(k); k != "" {
				out = append(out, adapt.Term{Text: k})
			}
		}
	}
	return out
}

var keywordsRe = regexp.MustCompile(`(?m)^## Keywords\s*\n+([^\n]+)`)
