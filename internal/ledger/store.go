package ledger

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Load reads the ledger. A missing file is an empty ledger, not an error: the
// first `snipe ledger` has nothing to merge into.
func Load(path string) (*Ledger, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Ledger{}, nil
	}
	if err != nil {
		return nil, err
	}
	var l Ledger
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// Save writes through a temp file and a rename. The ledger holds approvals that
// nothing else records, so a crash mid-write must leave the old file whole.
func Save(path string, l *Ledger) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cv-ledger-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// EntityNames are the technologies named by the facts that have not been
// rejected, sorted: what the CV demonstrates, since a fact is a claim about work
// and the skills catalogue is not in the ledger.
func (l *Ledger) EntityNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range l.Entries {
		for _, b := range e.Bullets {
			for _, f := range b.Facts {
				if f.Status == "rejected" {
					continue
				}
				for _, n := range f.Entities {
					if k := strings.ToLower(n); !seen[k] {
						seen[k] = true
						out = append(out, n)
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}
