package main

import (
	"math"
	"testing"
)

func TestLabelMetrics(t *testing.T) {
	truth := map[int]float64{1: 2, 2: 3, 3: 4, 4: 4}
	pred := map[int]float64{1: 1.5, 2: 4.0, 3: 4.0, 4: 3.0}
	// Five pairs differ in truth: (1,2) (1,3) (1,4) right, (2,3) tied, (2,4) wrong.
	if c, n := pairAccuracy(pred, truth); c != 3.5 || n != 5 {
		t.Fatalf("pair accuracy %v/%v, want 3.5/5", c, n)
	}
	if got := spearman(map[int]float64{1: 1, 2: 2, 3: 3}, map[int]float64{1: 10, 2: 20, 3: 30}); math.Abs(got-1) > 1e-9 {
		t.Fatalf("spearman of a monotone map %v, want 1", got)
	}
	if got := auc([]float64{5, 4}, []float64{4, 1}); got != 0.875 {
		t.Fatalf("auc %v, want 0.875", got)
	}
}
