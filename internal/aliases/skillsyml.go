package aliases

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

var (
	aliasesKeyRe = regexp.MustCompile(`^aliases:\s*(#.*)?$`)
	// An entry on one line: indent, key, then a [flow list] and maybe a comment.
	entryRe = regexp.MustCompile(`^(\s+)("(?:[^"\\]|\\.)*"|'[^']*'|[^:]+?)\s*:\s*(.*)$`)
	flowRe  = regexp.MustCompile(`^\[(.*)\](\s*#.*)?$`)
	plainRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+/()&-]*[A-Za-z0-9.+)]$|^[A-Za-z0-9]$`)
)

// AddAlias writes term under item in skills.yml by editing the text, so the
// user's comments and layout survive; a YAML round trip would drop both.
//
// The edit is parsed before anything is written, and it must change exactly
// one thing: item gains term. Anything the edit cannot place safely, it refuses
// and names, rather than guess at a hand-written file.
func AddAlias(path, item, term string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	src := string(b)
	before, err := parseAliases(src)
	if err != nil {
		return fmt.Errorf("%s does not parse: %w", path, err)
	}
	for _, f := range before[strings.ToLower(item)] {
		if strings.EqualFold(f, term) {
			return nil
		}
	}
	out, err := addAlias(src, item, term)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	after, err := parseAliases(out)
	if err != nil {
		return fmt.Errorf("%s: the edit would not parse (%v); add %q under %q by hand", path, err, term, item)
	}
	want := map[string][]string{}
	for k, v := range before {
		want[k] = v
	}
	want[strings.ToLower(item)] = append(slices.Clone(before[strings.ToLower(item)]), term)
	if !sameAliases(after, want) {
		return fmt.Errorf("%s: the edit would change more than %q; add it under %q by hand", path, term, item)
	}
	return writeAtomic(path, out)
}

// addAlias is the text edit itself: into the item's one-line [list] if it has
// one, else a new entry at the end of the aliases block, else a new block.
func addAlias(src, item, term string) (string, error) {
	lines := strings.Split(src, "\n")
	start := slices.IndexFunc(lines, aliasesKeyRe.MatchString)
	if start < 0 {
		s := strings.TrimRight(src, "\n")
		if s != "" {
			s += "\n"
		}
		return s + "aliases:\n  " + scalar(item) + ": [" + scalar(term) + "]\n", nil
	}
	last := start
	for i := start + 1; i < len(lines); i++ {
		ln := lines[i]
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if ln[0] != ' ' && ln[0] != '\t' {
			break // the next top-level key
		}
		last = i
		m := entryRe.FindStringSubmatch(ln)
		if m == nil || !strings.EqualFold(unquote(m[2]), item) {
			continue
		}
		f := flowRe.FindStringSubmatch(m[3])
		if f == nil {
			return "", fmt.Errorf("%q is not a one-line [list]; add %q to it by hand", item, term)
		}
		inner := strings.TrimSpace(f[1])
		if inner != "" {
			inner += ", "
		}
		lines[i] = m[1] + m[2] + ": [" + inner + scalar(term) + "]" + f[2]
		return strings.Join(lines, "\n"), nil
	}
	indent := "  "
	if last > start {
		indent = lines[last][:len(lines[last])-len(strings.TrimLeft(lines[last], " \t"))]
	}
	lines = slices.Insert(lines, last+1, indent+scalar(item)+": ["+scalar(term)+"]")
	return strings.Join(lines, "\n"), nil
}

// scalar writes s plain when that is unambiguous and quoted otherwise: "C#"
// has a comment character, "on" is a boolean to some parsers, "1.0" a number.
func scalar(s string) string {
	_, num := strconv.ParseFloat(s, 64)
	switch strings.ToLower(s) {
	case "true", "false", "null", "yes", "no", "on", "off", "y", "n", "~":
		return strconv.Quote(s)
	}
	if plainRe.MatchString(s) && num != nil {
		return s
	}
	return strconv.Quote(s)
}

func unquote(k string) string {
	k = strings.TrimSpace(k)
	if s, err := strconv.Unquote(k); err == nil && strings.HasPrefix(k, `"`) {
		return s
	}
	if len(k) >= 2 && k[0] == '\'' && k[len(k)-1] == '\'' {
		return strings.ReplaceAll(k[1:len(k)-1], "''", "'")
	}
	return k
}

// parseAliases reads the file the way config.Load does: keys lowercased, and
// two spellings of one key merged.
func parseAliases(src string) (map[string][]string, error) {
	var doc struct {
		Aliases map[string][]string `yaml:"aliases"`
	}
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for k, v := range doc.Aliases {
		key := strings.ToLower(strings.TrimSpace(k))
		out[key] = append(out[key], v...)
	}
	return out, nil
}

func sameAliases(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}
