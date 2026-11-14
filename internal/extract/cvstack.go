package extract

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/ollama"
)

// CVLanguages are the programming languages the CV demonstrates, in the same
// canonical form StackItem.Language uses, so the two sides compare by equality.
// The input is the ledger's entities (Entities): every technology named by a
// fact of a job or project, never the skills catalogue, so a language with no
// work behind it is not counted. Classifying a short list of terms is a task a
// 4B does reliably; reading the whole CV for languages was not (it dropped Java
// from a CV that names it fourteen times). One call per distinct list, cached
// with the posting replies.
func (e *Extractor) CVLanguages(ctx context.Context) ([]string, error) {
	return e.cvLanguages(ctx, true)
}

// CVLanguagesCached is CVLanguages that never calls the model (see Cached).
func (e *Extractor) CVLanguagesCached() ([]string, bool) {
	l, err := e.cvLanguages(context.Background(), false)
	return l, err == nil
}

func (e *Extractor) cvLanguages(ctx context.Context, call bool) ([]string, error) {
	if len(e.Entities) == 0 || e.CVSystem == "" {
		return nil, fmt.Errorf("extract: no CV entities to classify")
	}
	key := sum(e.CVSystem, strings.Join(e.Entities, "\x00"))
	e.mu.Lock()
	l, ok := e.cv[key]
	e.mu.Unlock()
	if ok {
		return l, nil
	}
	if !call || e.OL == nil {
		return nil, fmt.Errorf("extract: CV languages not in the cache")
	}
	var r struct {
		Terms []struct {
			Term     string `json:"term"`
			Language string `json:"language"`
		} `json:"terms"`
	}
	err := e.OL.Chat(ctx, ollama.Request{
		Model: e.Model, System: e.CVSystem,
		User: "## Terms\n- " + strings.Join(e.Entities, "\n- "),
		Schema: ollama.Object(ollama.Obj(
			ollama.F("terms", ollama.Arr(ollama.Object(ollama.Obj(
				ollama.F("term", capped(60)),
				ollama.F("language", ollama.Str(Languages...)),
			)), 200)),
		)),
		Options: ollama.Options{Temperature: 0, NumCtx: numCtx},
		Timeout: 3 * time.Minute, Label: "p1.cvstack",
	}, &r)
	if err != nil {
		return nil, err
	}
	langs := []string{} // empty, not nil: a CV with no languages is a known answer
	for _, x := range r.Terms {
		listed := slices.ContainsFunc(e.Entities, func(s string) bool { return strings.EqualFold(s, strings.TrimSpace(x.Term)) })
		if listed && x.Language != None && slices.Contains(Languages, x.Language) && !slices.Contains(langs, x.Language) {
			langs = append(langs, x.Language)
		}
	}
	e.mu.Lock()
	e.Calls++
	e.cv[key] = langs
	err = e.save()
	e.mu.Unlock()
	return langs, err
}
