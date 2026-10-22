package tui

import (
	"strings"
)

// wordDiff marks what changed between two lines, word by word: words only in
// old are drawn in the error colour in the first result, words only in new in
// the OK colour in the second. Words are split on spaces, so punctuation stays
// with the word it is attached to, and a changed word shows as one removed and
// one added.
func wordDiff(old, new string) (was, now string) {
	a, b := strings.Fields(old), strings.Fields(new)
	// Longest common subsequence of words.
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var wa, wb []string
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			wa, wb = append(wa, a[i]), append(wb, b[j])
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			wa = append(wa, styErr.Render(a[i]))
			i++
		default:
			wb = append(wb, styOK.Bold(true).Render(b[j]))
			j++
		}
	}
	for ; i < len(a); i++ {
		wa = append(wa, styErr.Render(a[i]))
	}
	for ; j < len(b); j++ {
		wb = append(wb, styOK.Bold(true).Render(b[j]))
	}
	return strings.Join(wa, " "), strings.Join(wb, " ")
}
