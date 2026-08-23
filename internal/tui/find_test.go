package tui

import (
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/state"
)

func TestSubseq(t *testing.T) {
	for _, c := range []struct {
		s, pat string
		want   bool
	}{
		{"Cisco — Backend Engineer", "cco", true},  // the whole point: gaps allowed
		{"Cisco — Backend Engineer", "ocs", false}, // order is not
		{"Cisco", "CISCO", true},                   // case-folded both ways
		{"Cisco", "", true},
		{"Cisco", "cisco!", false},
		{"Ünïcode — Rôle", "ünr", true}, // runes, not bytes
	} {
		if got := subseq(c.s, c.pat); got != c.want {
			t.Errorf("subseq(%q, %q) = %v, want %v", c.s, c.pat, got, c.want)
		}
	}
}

// Typing into the filter narrows the list, and everything indexed off it —
// cursor, current(), the day heading — follows the filtered slice rather than
// the session it came from.
func TestFindFiltersTheList(t *testing.T) {
	s := snap(map[int]state.Row{}, map[int]state.Offer{})
	s.session = []int{1, 2, 3}
	s.labels = map[int]string{1: "Acme — Engineer", 2: "Cisco — Backend", 3: "Datadog — SRE"}
	q := newTestQueue(t, s)

	if handled, _ := press(q, "f"); !handled || q.focus != fFind {
		t.Fatalf("f did not open the filter: handled=%v focus=%v", handled, q.focus)
	}
	for _, k := range []string{"c", "c", "o"} {
		press(q, k)
	}
	if len(q.visible) != 1 || q.visible[0] != 2 {
		t.Fatalf("cco left %v, want just the Cisco row", q.visible)
	}
	if q.snap.session[0] != 1 || len(q.snap.session) != 3 {
		t.Fatalf("the filter chewed up the snapshot: %v", q.snap.session)
	}

	press(q, "enter")
	if q.focus != fList {
		t.Fatalf("enter left focus at %v, want the list", q.focus)
	}
	if got := q.current().ID; got != 2 {
		t.Fatalf("the cursor landed on #%d, want the one visible row", got)
	}

	// A poll while the filter is up must not widen the list back out.
	q.setSnapshot(s)
	if len(q.visible) != 1 {
		t.Fatalf("a poll undid the filter: %v", q.visible)
	}

	if handled, _ := press(q, "esc"); !handled || len(q.visible) != 3 {
		t.Fatalf("esc did not clear the filter: %v", q.visible)
	}
}
