package text

import "testing"

// Go rounds halves to even, JavaScript rounds them away from zero. They agree
// everywhere except a tie, and a tie changed a real offer's judgment: coverage
// 0.225 renders 23% in Node and 22% under %.0f, one character inside the stage
// 3 prompt.
func TestFixedMatchesToFixed(t *testing.T) {
	for _, tc := range []struct {
		in     float64
		places int
		want   string
	}{
		{22.5, 0, "23"}, {23.5, 0, "24"}, {0.5, 0, "1"}, {1.5, 0, "2"},
		{22.4, 0, "22"}, {22.6, 0, "23"}, {67.0, 0, "67"},
		{2.345, 2, "2.35"}, {12.5, 0, "13"}, {0.225 * 100, 0, "23"}, // 1.005 is not exactly representable — the nearest double is just below it,
		// so Node returns "1.00" too. Taken from node, not from arithmetic.
		{1.005, 2, "1.00"},
	} {
		if got := Fixed(tc.in, tc.places); got != tc.want {
			t.Errorf("Fixed(%v, %d) = %s, want %s", tc.in, tc.places, got, tc.want)
		}
	}
}
