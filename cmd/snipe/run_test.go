package main

import "testing"

func TestParseRunWriter(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "verbatim"},
		{[]string{"--writer", "verbatim"}, "verbatim"},
		{[]string{"--writer=adapt", "--only-id", "7"}, "adapt"},
	} {
		opt, w, err := parseRun(c.args)
		if err != nil || w != c.want {
			t.Errorf("%v: writer %q, %v", c.args, w, err)
		}
		if len(c.args) == 3 && opt.OnlyID != 7 {
			t.Errorf("other flags lost: %+v", opt)
		}
	}
	if _, _, err := parseRun([]string{"--writer", "rewrite"}); err == nil {
		t.Error("an unknown writer was accepted")
	}
}
