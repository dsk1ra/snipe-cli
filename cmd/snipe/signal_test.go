package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// SIGUSR1 must cancel at once. This is the wire between the pause key and the
// in-flight request to Ollama: ollama.post builds its request with
// http.NewRequestWithContext, so a cancelled context is what tears the call
// down. Nothing else in the pause path does any tearing down.
func TestPauseSignalCancelsAtOnce(t *testing.T) {
	stopped := make(chan struct{}, 1)
	ctx, cleanup := signalCtx(func() { stopped <- struct{}{} })
	defer cleanup()

	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("SIGUSR1 did not cancel — the in-flight model call would run to completion")
	}
	// Pause does not ask the runner to finish anything; it drops the stage.
	select {
	case <-stopped:
		t.Error("pause asked for a graceful stop as well, which would let the stage finish")
	default:
	}
}

// SIGTERM is the other half: ask, do not cancel. The stage in flight gets to
// land so the resume does not redo it.
func TestStopSignalAsksFirst(t *testing.T) {
	stopped := make(chan struct{}, 1)
	ctx, cleanup := signalCtx(func() { stopped <- struct{}{} })
	defer cleanup()

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM did not ask the runner to stop")
	}
	select {
	case <-ctx.Done():
		t.Error("the first SIGTERM cancelled — the stage in flight is lost and gets redone")
	case <-time.After(100 * time.Millisecond):
	}

	// The second escalates: pause, arriving late.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the second SIGTERM did not cancel")
	}
}
