// Command gate compares this implementation against the Node one over the real
// cached corpus. Nothing in the port is "done" until its gate is green.
//
//	go run ./cmd/gate salary <jds-dir> <js-output.tsv>
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/text"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gate <salary|cleanjd|fit|atoms|select|tex|fill|p3|goldset|skills> <dir-or-file> [js-output]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "salary":
		os.Exit(gateOne(os.Args[2], os.Args[3], "salary", func(jd string) string {
			s := text.ExtractSalary(jd)
			if s == nil {
				return "null"
			}
			return fmt.Sprintf("%s|%d|%d|%s", s.Currency, s.Min, s.Max, s.Raw)
		}))
	case "cleanjd":
		os.Exit(gateOne(os.Args[2], os.Args[3], "cleanjd", func(jd string) string {
			out := text.CleanJD(jd, text.DefaultJDCap)
			sum := sha256.Sum256([]byte(out))
			return hex.EncodeToString(sum[:])[:16]
		}))
	case "p3":
		// gate p3 <home> <cv> <report> <jd> <out.json>
		os.Exit(gateP3(os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6]))
	case "p3norm":
		// gate p3norm <js-cv-content.json> <out.json>
		os.Exit(normalizeJS(os.Args[2], os.Args[3]))
	case "fill":
		os.Exit(gateFill(os.Args[2:]))
	case "tex":
		os.Exit(gateTex(os.Args[2]))
	case "select":
		os.Exit(gateSelect(os.Args[2], os.Args[3]))
	case "goldset":
		os.Exit(gateGoldset(os.Args[2:]))
	case "atoms":
		os.Exit(gateAtoms(os.Args[2], os.Args[3]))
	case "fit":
		os.Exit(gateFit(os.Args[2], os.Args[3]))
	case "skills":
		os.Exit(gateSkills(os.Args[2]))
	default:
		fmt.Fprintf(os.Stderr, "unknown gate %q\n", os.Args[1])
		os.Exit(2)
	}
}

func gateOne(jdsDir, jsFile, name string, fn func(string) string) int {
	want := map[string]string{}
	f, err := os.Open(jsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		file, val, ok := strings.Cut(sc.Text(), "\t")
		if ok {
			want[file] = val
		}
	}

	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)

	var diffs int
	for _, name := range names { //nolint:gocritic
		b, err := os.ReadFile(filepath.Join(jdsDir, name))
		if err != nil {
			continue
		}
		got := fn(string(b))
		if got != want[name] {
			diffs++
			if diffs <= 20 {
				fmt.Printf("%-10s go=%-40s js=%s\n", name, got, want[name])
			}
		}
	}
	fmt.Printf("\n%s: %d/%d identical", name, len(names)-diffs, len(names))
	if diffs > 0 {
		fmt.Printf(" — %d DIFFER\n", diffs)
		return 1
	}
	fmt.Println(" — GREEN")
	return 0
}
