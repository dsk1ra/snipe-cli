# hero.tape — the ~35 s loop that goes at the top of the README.
#
#   bash demo/record.sh hero
#
# Same sandbox and same fake pipeline as snipe.tape, but DEMO_SPEED shortens
# every phase: a README GIF autoplays on a page nobody scrolled to read, so it
# has one loop to show paste → run → score → applied, and the walkthrough can
# have the other three minutes.

Output demo/.sandbox/raw-hero.gif
Output demo/.sandbox/raw-hero.mp4

Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 18
Set Width 1380
Set Height 820
Set Padding 20
Set Margin 0
Set Theme "Catppuccin Mocha"
Set Framerate 24
Set TypingSpeed 55ms
Set WindowBar Colorful
Set WindowBarSize 36

Hide
Type "cd demo/.sandbox && export PATH=$PWD/bin:$PATH && export DEMO_SPEED=0.45 && clear" Enter
Type "snipe" Enter
Sleep 4s
Show

#> Every offer you have scanned, scored against your CV, on one screen
Sleep 3500ms

# tab from nothing focused lands in the JD box, which is where a paste has to go
#> Paste a job description
Tab
Sleep 600ms
Copy "Senior Backend Engineer at Helix Systems — Remote (EU). We run the settlement platform behind 40M transactions a day. You will own services written in Go and Java, deployed to Kubernetes on AWS, and the event pipeline (Kafka) that feeds them. What we are looking for: 5+ years building distributed backend systems; strong Go or JVM; Postgres and schema design under load; gRPC and REST API design; Terraform, CI/CD, observability (Prometheus, OpenTelemetry). Nice to have: Rust, event sourcing, payments or clearing domain experience. We offer EUR 95,000-120,000 depending on level, equity, and a hardware budget."
Paste
Sleep 2s
#> Enter through the URL and Add to queue — the run starts on its own
Enter
Copy "https://careers.helix.example.com/jobs/senior-backend-engineer-8821"
Paste
Sleep 1500ms
Enter
Sleep 600ms
Enter
Sleep 2500ms
#> Locally: pre-score → full evaluation → a CV tailored to the posting
Sleep 5s
#> 5.0 — report written, one-page PDF in output/
Sleep 3s

#> a marks it applied · the tracker row and the follow-up clock follow
Tab
Tab
Sleep 800ms
Type "a"
Sleep 3s

#> Every scan you have ever run, one square a day
Type "2"
Sleep 3500ms
#> Who is owed a nudge, worked out from the tracker
Type "3"
Sleep 3500ms
#> Local models, local files. Nothing leaves the machine.
Type "1"
Sleep 3s
Type "q"
Sleep 300ms
