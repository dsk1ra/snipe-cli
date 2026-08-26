package main

import (
	"bufio"
	"os"
	"sort"
	"strconv"
	"strings"
)

func readTSV(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "\t"); ok {
			out[k] = v
		}
	}
	return out, sc.Err()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
