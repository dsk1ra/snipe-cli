package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"charm.land/bubbletea/v2"

	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// msg reports the outcome of a keypress in the footer. It is feedback, not a
// log: it is swept after msgTTL rather than accumulating.
type msg struct {
	text  string
	isErr bool
}

const msgTTL = 5 * time.Second

func notef(format string, a ...any) tea.Cmd {
	return func() tea.Msg { return msg{text: fmt.Sprintf(format, a...)} }
}

func failf(format string, a ...any) tea.Cmd {
	return func() tea.Msg { return msg{text: fmt.Sprintf(format, a...), isErr: true} }
}

// openExternal hands a path or URL to the desktop. Detached and unwaited: the
// TUI must not block on somebody's editor.
func openExternal(target string) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("xdg-open", target)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return msg{text: "xdg-open: " + err.Error(), isErr: true}
		}
		go cmd.Wait() // reap it; nothing waits on the result
		return msg{text: "opened " + filepath.Base(target)}
	}
}

// runnerArgs is one invocation of our own `snipe run`.
type runnerArgs struct {
	args []string
	note string
}

// startRunner spawns `snipe run …` in its own session, so quitting the TUI
// leaves a long batch running — the same property the detached bash spawn had.
// Output goes to a log file because the child outlives the terminal.
//
// Two steps, in order: report the spawn, then watch for the run to become real.
// The note is what the reader gets immediately; awaitRunning is what turns the ▶
// into a spinner without waiting for the poll to come round.
func startRunner(l paths.Layout, ra runnerArgs) tea.Cmd {
	return tea.Sequence(spawnRunner(l, ra), awaitRunning(l))
}

// runSubcommand runs this binary's own CLI and brings the output back into the
// reader pane. It is what the / palette is made of.
//
// Foreground, unlike startRunner: these commands take no lock, there is nothing
// to resume, and the output IS the result. tea runs a Cmd in its own goroutine,
// so the UI keeps drawing and the pane heading keeps saying what is in flight.
func runSubcommand(l paths.Layout, argv []string) tea.Cmd {
	return func() tea.Msg {
		self, err := os.Executable()
		if err != nil {
			return detailMsg{err: fmt.Errorf("cannot find own binary: %w", err)}
		}
		cmd := exec.Command(self, argv...)
		cmd.Dir = l.Code
		// Combined: a subcommand's warnings go to stderr, and "0 new" with the
		// reason on a stream nobody read is the failure this pane exists to
		// stop.
		out, err := cmd.CombinedOutput()
		body := string(out)
		if err != nil {
			body += "\n" + err.Error()
		}
		if strings.TrimSpace(body) == "" {
			body = "(no output)"
		}
		return detailMsg{title: "/" + strings.Join(argv, " "), body: body}
	}
}

// refresh asks for a re-read with nothing to say. Used after a palette command,
// which has already reported through the reader pane.
func refresh() tea.Cmd {
	return func() tea.Msg { return refreshMsg{} }
}

// refreshMsg asks for a re-read and says nothing. It exists because msg carries
// footer text, and a second message from the same action would overwrite what
// that action already reported.
type refreshMsg struct{}

// runStartWait bounds the watch. It runs in its own goroutine and gives up
// without a word, so being generous costs nothing — and the child has a config
// load and a cv.md read to get through before it reaches Acquire.
const runStartWait = 5 * time.Second

// awaitRunning asks for a re-read once the spawned child actually holds the
// lock.
//
// The ▶ becomes a spinner when a run holds the lock, and the TUI cannot take
// that lock on the child's behalf — the child takes it, some milliseconds after
// Start() returns. Without this the glyph sat unchanged until the next
// one-second tick, so launching a run looked like a keypress that did nothing.
func awaitRunning(l paths.Layout) tea.Cmd {
	return func() tea.Msg {
		for deadline := time.Now().Add(runStartWait); time.Now().Before(deadline); {
			if state.Active(l.Lock()) {
				return refreshMsg{}
			}
			time.Sleep(15 * time.Millisecond)
		}
		// It never started. The log says why, and the note the spawn already
		// sent is the last word — returning nothing leaves it standing.
		return nil
	}
}

func spawnRunner(l paths.Layout, ra runnerArgs) tea.Cmd {
	return func() tea.Msg {
		self, err := os.Executable()
		if err != nil {
			return msg{text: "cannot find own binary: " + err.Error(), isErr: true}
		}
		if err := os.MkdirAll(l.Logs(), 0o755); err != nil {
			return msg{text: err.Error(), isErr: true}
		}
		logPath := filepath.Join(l.Logs(), "run-"+time.Now().Format("20060102-150405")+".log")
		f, err := os.Create(logPath)
		if err != nil {
			return msg{text: err.Error(), isErr: true}
		}
		defer f.Close()

		cmd := exec.Command(self, append([]string{"run"}, ra.args...)...)
		cmd.Dir = l.Code
		cmd.Stdout, cmd.Stderr = f, f
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return msg{text: err.Error(), isErr: true}
		}
		go cmd.Wait()
		return msg{text: ra.note}
	}
}

// stopRunner asks the active run to stop after the stage it is in. That stage
// is checkpointed first, so a resume starts at the next one and nothing is
// redone — the price is waiting for a model call that may have minutes left.
//
// pauseRunner is the other half, for when you are not waiting.
//
// This is the whole of the stop mechanism. The lock file names the process, and
// a signal is enough — no control socket, no daemon.
func stopRunner(l paths.Layout) tea.Cmd {
	return signalRun(l, syscall.SIGTERM, func(int) string {
		return "stopping after the current stage — ▶ resumes there"
	})
}

// pauseRunner drops the stage in flight and keeps every stage behind it.
//
// The cost is one model call, not one phase: a Phase 2 offer paused in the
// judgment stage keeps its parse and its evidence, both keyed by their inputs,
// and resume runs the judgment alone. What it says is where it stopped, read
// back out of the checkpoints — a pause that cannot tell you what it dropped is
// a pause you have to verify by watching the next run.
//
// It sends SIGUSR1 rather than a second SIGTERM. The two mean opposite things
// about the stage in flight — keep it or drop it — and deciding between them on
// the timing of two signals is how a pause becomes a stop on a slow day.
func pauseRunner(l paths.Layout) tea.Cmd {
	return signalRun(l, syscall.SIGUSR1, func(pid int) string {
		w := state.Interrupted(l.Work())
		if len(w) == 0 {
			return "paused — ▶ resumes"
		}
		if len(w) == 1 {
			return fmt.Sprintf("paused — #%d dropped %s, ▶ runs it again", w[0].ID, w[0].Stage)
		}
		return fmt.Sprintf("paused — %d stages dropped, ▶ runs them again", len(w))
	})
}

func signalRun(l paths.Layout, sig syscall.Signal, note func(pid int) string) tea.Cmd {
	return func() tea.Msg {
		if !state.Active(l.Lock()) {
			return msg{text: "nothing is running", isErr: true}
		}
		pid, ok := state.HolderPID(l.Lock())
		if !ok {
			return msg{text: "a run holds the lock but recorded no PID", isErr: true}
		}
		if err := syscall.Kill(pid, sig); err != nil {
			return msg{text: fmt.Sprintf("signalling %d: %v", pid, err), isErr: true}
		}
		return msg{text: note(pid)}
	}
}

// enqueueJD adds a pasted JD as a new offer. The text is written to
// batch/jds/<id>.txt, which is what all three phases read — the link is stored
// for tracking and is never fetched.
func enqueueJD(l paths.Layout, jd, link string) (int, error) {
	jd = strings.TrimSpace(jd)
	if len(jd) < 100 {
		return 0, fmt.Errorf("JD too short (%d chars) — paste the full description", len(jd))
	}
	offers, err := state.LoadOffers(l.Input())
	if err != nil {
		return 0, err
	}
	if link != "" {
		for _, o := range offers {
			if o.URL == link {
				return 0, fmt.Errorf("link already in the pipeline as #%d", o.ID)
			}
		}
	}
	id := state.NextOfferID(offers)
	url := link
	if url == "" {
		url = "manual:snipe-" + strconv.Itoa(id)
	}
	if err := os.MkdirAll(l.JDs(), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(l.JDs(), strconv.Itoa(id)+".txt"), []byte(jd), 0o644); err != nil {
		return 0, err
	}
	return id, state.AppendOffer(l.Input(), state.Offer{
		ID: id, URL: url, Source: "snipe",
		Notes: "manual JD " + time.Now().Format("2006-01-02"),
	})
}

// appendQueue adds an id to the drain queue, for when a run already holds the
// lock. O_APPEND on a line-sized write is atomic enough here; the alternative
// was the bash version's flock dance around a rewrite.
func appendQueue(path string, id int) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d\n", id)
	return err
}

// toggleMark flips an applied/skipped mark and persists it. The marks live on
// disk because the row list is rebuilt from local-state.tsv every second — an
// in-memory mark would come straight back.
func toggleMark(path string, marks state.Marks, id int) (bool, error) {
	if _, on := marks[id]; on {
		delete(marks, id)
		return false, marks.Save(path)
	}
	marks[id] = time.Now()
	return true, marks.Save(path)
}

// debugTarget is the *input* the failed phase read, to be edited in place before
// the retry reads it back — the fetched JD for Phases 1–2, the Phase 2 report
// for Phase 3. It falls back to the phase's own payload when the input never
// landed, which is itself the diagnosis.
func debugTarget(l paths.Layout, c *state.Cache, id int, r state.Row) (string, bool) {
	nonEmpty := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && st.Size() > 0
	}
	jd := filepath.Join(l.JDs(), strconv.Itoa(id)+".txt")
	switch state.FailedPhase(r) {
	case "p1", "p2":
		if nonEmpty(jd) {
			return jd, true
		}
		payload := filepath.Join(l.Scores(), strconv.Itoa(id)+".json")
		if state.FailedPhase(r) == "p2" {
			payload = filepath.Join(l.Evals(), strconv.Itoa(id)+".json")
		}
		if nonEmpty(payload) {
			return payload, true
		}
	case "p3":
		if p, ok := c.ReportFile(l.Reports(), r.ReportNum); ok {
			return p, true
		}
		if nonEmpty(jd) {
			return jd, true
		}
	}
	return "", false
}
