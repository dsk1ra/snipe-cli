# screens.tape — the README gallery. Stills only; the gif output is a throwaway.
#
#   bash demo/record.sh screens
#
# VHS takes a Screenshot at the start of the *next* command, so every one is
# followed by a short Sleep — without it the shot lands one keypress late, which
# is how a "queue" screenshot ends up showing the Activity tab.

Output demo/.sandbox/screens.gif

Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 18
Set Width 1380
Set Height 820
Set Padding 20
Set Margin 0
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set WindowBarSize 36

Hide
Type "cd demo/.sandbox && export PATH=$PWD/bin:$PATH && clear" Enter
Type "snipe" Enter
Sleep 4s
Show

# 1 · the command palette, mid-command
Type "/"
Sleep 500ms
Type "scan"
Sleep 1500ms
Screenshot demo/screens/palette.png
Sleep 500ms
Enter
Sleep 6s
Escape
Sleep 1s

# 2 · the queue, on the failed row, so a row's inline actions are on show
Down
Up
Up
Up
Sleep 2s
Screenshot demo/screens/queue.png
Sleep 500ms

# 3 · the report reader — the one thing the ink version could not do at all.
# Down lands on a finished row: a gated one has no report to open.
Down
Type "v"
Sleep 2500ms
Screenshot demo/screens/report.png
Sleep 500ms
Escape
Sleep 500ms

# 4 · a run in flight — ▶ drains the two the scan queued
Tab
Tab
Enter
Sleep 6s
Screenshot demo/screens/running.png
Sleep 500ms

# 5 · the year heatmap
Type "2"
Sleep 2500ms
Screenshot demo/screens/activity-year.png
Sleep 500ms

# 6 · one day, hour by hour
Type "d"
Sleep 2s
Screenshot demo/screens/activity-day.png
Sleep 500ms

# 7 · the follow-up board
Type "3"
Sleep 3s
Screenshot demo/screens/followups.png
Sleep 500ms

# 8 · the key reference
Type "?"
Sleep 2s
Screenshot demo/screens/keys.png
Sleep 500ms

Escape
Type "q"
Sleep 500ms
