// Command sandbox is the demo rig: one binary that seeds demo/.sandbox with
// invented data, stands in for `snipe run` and `snipe scan` against it, and
// launches the REAL TUI on top.
//
//	go build -o demo/.sandbox/snipe ./demo/sandbox   # build it into the sandbox
//	demo/.sandbox/snipe seed                          # (re)build the fixture
//	demo/.sandbox/snipe                               # the TUI, on the fixture
//
// The TUI spawns its runner by re-executing os.Executable() with `run …`, and
// resolves both roots from the binary's own directory — so a binary sitting in
// demo/.sandbox drives the fixture and nothing else. No Ollama, no network, no
// cv.md, and the real data/ tree is never opened.
//
// Everything on screen is invented: the companies, the roles, the scores, the
// applications and the follow-ups all come from seed.go, and the pipeline behind
// them is sim() below — the same files the real pipeline writes, on a timer.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/tui"
)

// root is the sandbox: the directory the binary itself sits in. Both of the
// layout's roots point there, so the fixture is self-contained and SNIPE_HOME
// is not needed — one less thing a tape can get wrong.
func root() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

func layout() paths.Layout {
	r := root()
	return paths.Layout{Home: r, Code: r}
}

func main() {
	cmd, args := "", []string(nil)
	if len(os.Args) > 1 {
		cmd, args = os.Args[1], os.Args[2:]
	}
	var err error
	switch cmd {
	case "":
		p := tea.NewProgram(tui.New(layout()))
		_, err = p.Run()
	case "seed":
		err = seed(args)
	case "run":
		err = sim(layout(), args)
	case "scan":
		err = fakeScan(layout())
	default:
		err = fmt.Errorf("unknown command %q — seed | run | scan | (none, for the TUI)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox: "+err.Error())
		os.Exit(1)
	}
}
