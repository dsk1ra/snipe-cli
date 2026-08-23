package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrLocked is returned when another runner already holds the lock.
var ErrLocked = errors.New("another snipe run is already active")

// Lock is the single-runner mutex, an advisory flock on batch/local-runner.pid.
//
// The Node version used the file's mere existence plus `kill -0` on its
// contents. That goes stale two ways: a SIGKILLed runner leaves the file behind,
// and a recycled PID makes a dead runner look alive. A flock is released by the
// kernel when the holder dies, however it dies.
//
// The PID is still written into the file, because the TUI needs something to
// signal for a graceful stop — the flock says *whether* a run is active, the
// contents say *whom* to ask to stop.
type Lock struct{ f *os.File }

func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if pid, ok := HolderPID(path); ok {
				return nil, fmt.Errorf("%w (PID %d)", ErrLocked, pid)
			}
			return nil, ErrLocked
		}
		return nil, err
	}
	// Truncate then write: a shorter PID must not leave the old one's tail.
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release drops the lock and removes the file. Closing alone would release the
// flock; the file goes too so nothing reads a PID that no longer runs anything.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	name := l.f.Name()
	err := l.f.Close()
	os.Remove(name)
	l.f = nil
	return err
}

// HolderPID reads the PID recorded in the lock file. It does not prove the
// holder is alive — pair it with Active.
func HolderPID(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// Active probes the lock without taking it: true when a runner holds it right
// now. This is what the TUI polls, and unlike the old `kill -0` it cannot be
// fooled by a stale file or a recycled PID.
func Active(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return false // no file, no holder
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	// We got it, so nobody held it. Drop it again immediately.
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}
