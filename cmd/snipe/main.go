// Command snipe is the whole tool: the TUI, the three-phase runner, and the
// handful of maintenance commands around them.
//
// One binary replaces node + bash + ink + playwright. Subcommand dispatch is
// stdlib flag — there are a dozen commands and no need for shell completion
// machinery yet.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

const usage = `snipe — local job-search pipeline, fully offline against Ollama

  snipe               the TUI (default)
  snipe run           the three-phase pipeline
  snipe scan          fetch portals.yml's and the registry's boards, queue what is new
  snipe registry      list UK employers from public data, and the ATS behind each careers page
  snipe add           add a pasted JD and run it
  snipe stop          ask the active run to stop after its current stage
  snipe pause         drop the stage in flight; resume runs it again
  snipe status        what is running, and where it is
  snipe doctor        check the setup and reconcile state against the artifacts
  snipe tracker fix   canonical statuses, one row per opening, working report links
  snipe build         rebuild the binary from this checkout, then run it

Run a subcommand with -h for its flags.
`

func main() {
	// Before anything dispatches, so os.Getenv is the one way a key is read.
	config.LoadDotEnv(filepath.Join(layout().Home, ".env"))
	if err := dispatch(); err != nil {
		fmt.Fprintln(os.Stderr, "snipe: "+err.Error())
		os.Exit(1)
	}
}

func dispatch() error {
	cmd, args := "", []string(nil)
	if len(os.Args) > 1 {
		cmd, args = os.Args[1], os.Args[2:]
	}

	switch cmd {
	case "", "tui":
		return cmdTUI()
	case "run":
		return cmdRun(args)
	case "scan":
		return cmdScan(args)
	case "registry":
		return cmdRegistry(args)
	case "add":
		return cmdAdd(args)
	case "stop":
		return cmdStop(args)
	case "pause":
		return cmdPause()
	case "status":
		return cmdStatus()
	case "doctor":
		return cmdDoctor()
	case "tracker":
		return cmdTracker(args)
	case "build":
		return cmdBuild(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// layout resolves the data and code roots. The code root is the binary's own
// directory when it sits in a checkout, so prompts/ and templates/ are found
// without configuration; SNIPE_HOME splits the data root off for tests.
func layout() paths.Layout {
	code := ""
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "prompts")); err == nil {
			code = dir
		}
	}
	if code == "" {
		code, _ = os.Getwd()
	}
	return paths.Default(code)
}

// cmdBuild is the edit-run loop: rebuild this binary in place, then become it.
// Anything after `build` is the command line the new binary gets, so
// `snipe build` lands in the TUI and `snipe build run --drain` starts the run.
//
// Exec rather than spawn — one process, so the terminal, the signals and the
// exit code all still belong to the thing you asked for.
func cmdBuild(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// go build unlinks the output before writing it, so overwriting the binary
	// that is running this is fine: we keep the old inode until the exec.
	build := exec.Command("go", "build", "-o", exe, "./cmd/snipe")
	build.Dir = layout().Code
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("go build in %s: %w", build.Dir, err)
	}
	return syscall.Exec(exe, append([]string{exe}, args...), os.Environ())
}

// signalCtx wires the two ways a run ends early.
//
// SIGUSR1 is PAUSE: cancel at once, which tears down the in-flight HTTP request
// to Ollama and any child process. What that costs is one stage — for a Phase 2
// offer, one of three model calls rather than all three, because every stage
// that landed is on disk and keyed by its input. The dropped stage is named in
// the checkpoint and runs again on resume.
//
// SIGINT/SIGTERM is the graceful stop: onStop asks the runner to finish the
// stage it is in, checkpoint it and exit, so even that stage is not redone. A
// second one cancels — pause, arriving late, for a stage that turned out to be
// slower than the patience for it. A third is left to the default handler, so a
// wedged process is still killable the usual way.
//
// Pause gets its own signal rather than being "SIGTERM twice" because the two
// mean opposite things about the stage in flight, and a stop that escalates on
// a timer is a stop that sometimes does the other one.
func signalCtx(onStop func()) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGUSR1)
	go func() {
		defer signal.Stop(ch)
		if <-ch == syscall.SIGUSR1 {
			fmt.Fprintln(os.Stderr, "\npaused — the stage in flight is dropped and runs again on resume")
			cancel()
			return
		}
		fmt.Fprintln(os.Stderr, "\nstopping after the current stage — signal again to drop it")
		onStop()
		<-ch
		fmt.Fprintln(os.Stderr, "dropping the stage in flight")
		cancel()
	}()
	return ctx, func() { signal.Stop(ch); cancel() }
}

// holder is the PID of the live run, or an error naming which of the two things
// went wrong: nothing running, or a lock with no PID in it.
func holder(l paths.Layout) (int, error) {
	if !state.Active(l.Lock()) {
		return 0, errors.New("nothing is running")
	}
	pid, ok := state.HolderPID(l.Lock())
	if !ok {
		return 0, errors.New("a run holds the lock but recorded no PID")
	}
	return pid, nil
}

func cmdStop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	now := fs.Bool("now", false, "drop the in-flight stage instead of letting it finish (same as `snipe pause`)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *now {
		return cmdPause()
	}
	l := layout()
	pid, err := holder(l)
	if err != nil {
		return err
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signalling %d: %w", pid, err)
	}
	fmt.Printf("asked %d to stop after its current stage — `snipe run` resumes there\n", pid)
	return nil
}

// cmdPause drops the stage in flight and keeps everything already checkpointed.
//
// The stage is named in each offer's checkpoint, so the resume line says where
// it picks up rather than leaving you to infer it from what did not happen.
func cmdPause() error {
	l := layout()
	pid, err := holder(l)
	if err != nil {
		return err
	}
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		return fmt.Errorf("signalling %d: %w", pid, err)
	}
	fmt.Printf("paused %d\n", pid)
	for _, w := range state.Interrupted(l.Work()) {
		fmt.Printf("  #%d dropped %s — `snipe run` starts there\n", w.ID, w.Stage)
	}
	return nil
}

func cmdStatus() error {
	l := layout()
	rows, err := state.Load(l.State())
	if err != nil {
		return err
	}
	// The same checkpoint marks say two different things depending on whether
	// anything is still holding the lock: a stage in flight, or a stage dropped.
	inFlight := state.Interrupted(l.Work())
	running := state.Active(l.Lock())
	switch {
	case running:
		pid, _ := state.HolderPID(l.Lock())
		fmt.Printf("running (PID %d)\n", pid)
		for _, w := range inFlight {
			fmt.Printf("  #%d in %s\n", w.ID, w.Stage)
		}
	case len(inFlight) > 0:
		fmt.Println("paused — `snipe run` starts at the dropped stages")
		for _, w := range inFlight {
			fmt.Printf("  #%d dropped %s\n", w.ID, w.Stage)
		}
	default:
		fmt.Println("idle")
	}
	if q := state.Queue(l.Queue()); len(q) > 0 {
		fmt.Printf("queued: %v\n", q)
	}
	s := state.Lifetime(rows)
	fmt.Printf("%d offers · %d evaluated · %d CVs · %d gated · %d failed\n",
		len(rows), s.Completed, s.CV, s.Gated, s.Failed)

	// Anything with a partial checkpoint is work a restart would resume rather
	// than redo. Worth naming: it is the difference between "stopped" and "lost".
	ents, _ := os.ReadDir(l.Work())
	for _, e := range ents {
		var id int
		if _, err := fmt.Sscanf(e.Name(), "%d.json", &id); err != nil {
			continue
		}
		if done := state.LoadCheckpoint(l.Work(), id).Done(); len(done) > 0 {
			if r, ok := rows[id]; ok && r.P3Status != state.P3Completed && r.P3Status != state.P3Skipped {
				fmt.Printf("  #%d resumable past %v\n", id, done)
			}
		}
	}
	return nil
}
