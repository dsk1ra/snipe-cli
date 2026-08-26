#!/usr/bin/env bash
# record.sh — rebuild every demo artifact, from the repo root.
#
#   bash demo/record.sh            # hero + walkthrough + screenshots (~10 min)
#   bash demo/record.sh hero       # just one of them
#
# Each recording re-seeds first: the tapes drive a session that mutates the
# sandbox (queues offers, runs them, marks one applied, nudges a follow-up), so a
# tape run against a used sandbox starts from a different screen and its fixed
# sleeps drift out of step with what is happening.
set -euo pipefail
cd "$(dirname "$0")/.."

want=${1:-all}
run() { [[ "$want" == "all" || "$want" == "$1" ]]; }

# One build, then the binary is moved INTO the fixture it just seeded: the TUI
# resolves both of its roots from its own path, so a binary living in
# demo/.sandbox drives the sandbox and can reach nothing else.
seed() {
  go build -o demo/.sandbox.bin ./demo/sandbox
  ./demo/.sandbox.bin seed -o demo/.sandbox
  mv demo/.sandbox.bin demo/.sandbox/snipe
  ln -sf ../snipe demo/.sandbox/bin/snipe   # so the tapes can type `snipe scan`
}

if run hero; then
  echo "── hero (README loop) ──"
  seed
  vhs demo/hero.tape
  go run ./demo/caption demo/hero.tape
fi

if run walkthrough; then
  echo "── walkthrough (full session) ──"
  seed
  vhs demo/snipe.tape
  go run ./demo/caption demo/snipe.tape
fi

if run screens; then
  echo "── screenshots ──"
  mkdir -p demo/screens
  seed
  vhs demo/screens.tape
  ls -1 demo/screens
fi
