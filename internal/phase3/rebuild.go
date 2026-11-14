package phase3

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/suggest"
)

// finished is what Run left in an offer's output folder.
type finished struct {
	dir     string
	content fill.Content
	jd      string
	role    string
}

func (t *Tailor) finished(id int) (finished, error) {
	row, ok := t.Store.Get(id)
	if !ok {
		return finished{}, fmt.Errorf("no state row")
	}
	dir, ok := t.Cache.ResultDir(t.L.Output(), row.ReportNum)
	if !ok {
		return finished{}, fmt.Errorf("#%d has no CV folder in output/ yet", id)
	}
	body, err := os.ReadFile(filepath.Join(dir, "cv-content.json"))
	if err != nil {
		return finished{}, err
	}
	content, err := fill.ParseContent(body)
	if err != nil {
		return finished{}, fmt.Errorf("cv-content.json: %w", err)
	}
	jd, err := os.ReadFile(filepath.Join(dir, "job-description.txt"))
	if err != nil {
		return finished{}, err
	}
	f := finished{dir: dir, content: content, jd: string(jd)}
	if ev := t.Cache.Eval(t.L.Evals(), id); ev != nil {
		f.role = ev.Role
	}
	return f, nil
}

// Suggest asks the model how the finished CV's bullets and skills could be
// worded for this posting, and leaves the answer in the offer's folder as
// suggestions.json. Earlier decisions are discarded: they were about earlier
// suggestions.
func (t *Tailor) Suggest(ctx context.Context, id int) (int, error) {
	f, err := t.finished(id)
	if err != nil {
		return 0, err
	}
	system, err := os.ReadFile(t.L.Prompt("p3-suggest.md"))
	if err != nil {
		return 0, err
	}
	items, err := suggest.Generate(ctx, t.OL, id, suggest.Input{
		Content: f.content, CVText: t.Cfg.CVRaw, JD: f.jd, System: string(system),
	})
	if err != nil {
		return 0, err
	}
	// The offers to put the person's own line back after an adaptation are not
	// model suggestions and their decisions stand.
	old, _ := suggest.Load(f.dir)
	var keep []suggest.Item
	for _, it := range old {
		if strings.HasPrefix(it.ID, suggest.AdaptPrefix) {
			keep = append(keep, it)
		}
	}
	return len(items), suggest.Save(f.dir, append(keep, items...))
}

// Rebuild renders the PDF again with the accepted suggestions swapped in. The
// cv-content.json on disk is left as Run wrote it, so a rejection after the fact
// and a second rebuild gets the original back.
func (t *Tailor) Rebuild(ctx context.Context, id int) (accepted int, pages int, err error) {
	f, err := t.finished(id)
	if err != nil {
		return 0, 0, err
	}
	items, err := suggest.Load(f.dir)
	if err != nil {
		return 0, 0, err
	}
	tpl, err := os.ReadFile(t.L.Template("cv-template.tex"))
	if err != nil {
		return 0, 0, err
	}
	content, n := suggest.Apply(f.content, items)
	fit, _, err := t.compile(ctx, f.dir, content, f.jd, f.role, string(tpl), suggest.Approved(items))
	if err != nil {
		return 0, 0, err
	}
	return n, fit.Pages, nil
}
