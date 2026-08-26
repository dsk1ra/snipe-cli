# snipe TUI demo — a full session, recorded against demo/.sandbox.
#
#   bash demo/record.sh walkthrough
#
# Everything on screen is invented: the companies, the scores, the applications,
# the follow-ups. The pipeline behind it is demo/sandbox, which writes the files
# the real one writes, on a timer. No model runs, no posting is fetched, no CV is
# read.
#
# `#>` lines are caption cues, read by demo/caption — it re-walks this tape to
# work out when each one is on screen, so a caption cannot drift away from the
# keystroke it describes. `#>` alone clears the strip. VHS ignores both.
#
# The sleeps are counted against the phase durations at the top of
# demo/sandbox/sim.go: p1 is 2.8 s, p2 5.4 s, p3 4.1 s. Change one and the other
# needs a look.

Output demo/.sandbox/raw-demo.mp4

Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 18
Set Width 1380
Set Height 820
Set Padding 20
Set Margin 0
Set Theme "Catppuccin Mocha"
Set Framerate 24
Set TypingSpeed 60ms
Set WindowBar Colorful
Set WindowBarSize 36

Hide
Type "cd demo/.sandbox && export PATH=$PWD/bin:$PATH && clear" Enter
Type "snipe" Enter
Sleep 4s
Show

# ── the board ────────────────────────────────────────────────────────────────
#> Everything the pipeline knows, read off disk once a second
Sleep 3500ms
#> Left: the day's goals, over the pipeline's lifetime counters
Sleep 4s
#> Right: every offer, newest at the bottom, grouped by the day it arrived
Sleep 4s
#> ✓ done · ? gated · ✗ failed · ! stopped half way — and it spins while it runs
Sleep 4500ms

# ── the scanner, from the command palette ────────────────────────────────────
#> / is the CLI, without leaving the TUI
Type "/"
Sleep 1200ms
Type "scan"
Sleep 1500ms
#> scan sweeps the job boards. No model, no tokens — a URL is enough
Enter
Sleep 4s
#> Two new postings queued, and the whole of what it dropped to get there
Sleep 4s
Escape
Sleep 1500ms

# ── the rows ─────────────────────────────────────────────────────────────────
#> ↓ drops into the list, at the newest row
Down
Sleep 2500ms
#> #597 and #598 are the scan's finds — queued, not yet scored
Up
Sleep 2500ms
Up
Sleep 1500ms
#> v reads the report inside the tool. No editor, no browser
Type "v"
Sleep 4s
Down
Down
Down
Sleep 3500ms
Escape
Sleep 1500ms

#> A failed offer carries what to do next: see error · retry · debug
Up
Sleep 3s
#> → walks the row's own actions. Enter fires the focused one
Right
Sleep 1200ms
Enter
Sleep 4500ms
#> The stage that failed, and what it was holding when it did
Sleep 3s
Escape
Sleep 1200ms
#> retry re-runs the offer from scratch, overriding every phase guard
Right
Sleep 3s
#> debug opens the input that phase actually read, to fix and run again
Right
Sleep 3s
Left
Left
Left
Sleep 1200ms

# ── the Phase 1 gate ─────────────────────────────────────────────────────────
#> This one was gated: 1.9 from the 4B pre-screen, under the Phase 2 threshold
Up
Sleep 3500ms
#> A gate is a question, not a verdict — y evaluates it anyway, n dismisses it
Right
Sleep 3s
Type "y"
Sleep 4s
#> Phase 2 runs on the score it already had. Nothing is re-scored
Sleep 5s
#> 3.9 — the gate was wrong about this one, and it has a CV to show for it
Sleep 4s

#> x retires a row you are not going to apply to
Up
Sleep 2500ms
Type "x"
Sleep 2500ms
#> P2 2.9 — evaluated, and deliberately no CV: under the Phase 3 threshold
Sleep 3s

# ── run the queue ────────────────────────────────────────────────────────────
#> tab cycles the three groups: the inputs, ▶, and the list
Tab
Sleep 1500ms
Tab
Sleep 1500ms
#> ▶ runs the queue: pre-score → full evaluation → a CV tailored to the posting
Enter
Sleep 5s
#> The scan's first find has a name and a score now — the run gave it both
Sleep 8s
#> Each offer clears all three phases before the next one starts
Sleep 5s
#> #598 stopped at the gate on its own: 2.1 is under the Phase 2 threshold
Sleep 4s

# ── paste a job description ──────────────────────────────────────────────────
#> Paste a job description straight into the box
Tab
Tab
Sleep 1200ms
Copy "Senior Backend Engineer at Helix Systems — Remote (EU). We run the settlement platform behind 40M transactions a day. You will own services written in Go and Java, deployed to Kubernetes on AWS, and the event pipeline (Kafka) that feeds them. What we are looking for: 5+ years building distributed backend systems; strong Go or JVM; Postgres and schema design under load; gRPC and REST API design; Terraform, CI/CD, observability (Prometheus, OpenTelemetry). Nice to have: Rust, event sourcing, payments or clearing domain experience. We offer EUR 95,000-120,000 depending on level, equity, and a hardware budget."
Paste
Sleep 2500ms
#> Enter steps on — the posting's URL is optional, and never fetched
Enter
Sleep 800ms
Copy "https://careers.helix.example.com/jobs/senior-backend-engineer-8821"
Paste
Sleep 2000ms
#> Enter again, then Enter on Add to queue — and it starts on its own
Enter
Sleep 1000ms
Enter
Sleep 5s

# ── pause and resume ─────────────────────────────────────────────────────────
#> p drops the stage in flight. s is the other one: it lets the stage finish
Escape
Sleep 600ms
Type "p"
Sleep 4s
#> One model call thrown away, not one phase — everything behind it is on disk
Sleep 3s
#> ▶ resumes at the dropped stage. Phase 1 is not run again
Tab
Tab
Sleep 1200ms
Enter
Sleep 6s
#> 5.0 — report written, one-page PDF in output/
Sleep 6s

#> a marks it applied — the tracker row and the follow-up clock follow
Tab
Sleep 1200ms
Type "a"
Sleep 3500ms

# ── activity ─────────────────────────────────────────────────────────────────
#> 2 — Activity: a year of scanning, one square a day
Type "2"
Sleep 4s
#> m — one month, day by day
Type "m"
Sleep 3s
#> d — one day, hour by hour
Type "d"
Sleep 3s
Type "y"
Sleep 1500ms
#> tab swaps the heatmap between scans and applications sent
Tab
Sleep 3500ms
Tab
Sleep 1500ms
#> enter drops a cursor into the grid · ← → a week at a time, ↑ ↓ a day
Enter
Sleep 1500ms
Left
Sleep 1200ms
Left
Sleep 2500ms
#> o opens that day in the queue — the rows behind the square
Type "o"
Sleep 4s

# ── follow-ups ───────────────────────────────────────────────────────────────
#> 3 — Follow-ups: who is owed a nudge, worked out from the tracker
Type "3"
Sleep 4s
#> urgent · overdue · waiting · cold — from the cadence, not a reminder you set
Down
Sleep 3s
#> o reads that application's report — the same pane, borrowed from any tab
Type "o"
Sleep 4s
#> esc gives the board back, with the cursor where it was
Escape
Sleep 3s
#> enter records the nudge in data/follow-ups.md, which resets its own clock
Down
Down
Sleep 2500ms
Enter
Sleep 4s
#> p advances the stage: applied → responded → interview → offer
Down
Sleep 1500ms
Type "p"
Sleep 4s
# r starts a 5 s grace timer, so u has to land inside it — keep this pair short
#> r ends it as rejected — with five seconds to take it back
Down
Sleep 1500ms
Type "r"
Sleep 3s
#> u takes it back, inside the window
Type "u"
Sleep 4s

# ── the key reference ────────────────────────────────────────────────────────
#> ? — every key the focused tab knows, from the bindings the footer renders
Type "?"
Sleep 5s
Escape
Sleep 800ms
#> Local models, local files. Nothing leaves the machine.
Type "1"
Sleep 5s
# short tail: a half-drawn shell prompt is a poor last frame
Type "q"
Sleep 300ms
