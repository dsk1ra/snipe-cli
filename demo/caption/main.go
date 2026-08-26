// Command caption burns a tape's `#>` cues into the recording as a caption
// strip below the terminal.
//
//	go run ./demo/caption demo/snipe.tape
//
// VHS has no caption command, so this pads the frame and draws the text into the
// new strip with ffmpeg — nothing the TUI drew is covered.
//
// The timings are not written down anywhere: it re-walks the tape and adds up
// what each command costs (a Sleep is its own duration, a Type is one
// TypingSpeed per character, a key press is one), then scales that timeline onto
// the recording's real duration to absorb the per-keystroke overhead VHS adds.
// So a cue stays attached to the keystroke it sits above, and editing the tape
// cannot silently desync the captions from the actions.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	strip   = 84         // caption strip, in pixels below the terminal
	bg      = "0x181825" // Catppuccin Mocha mantle — a shade under the terminal
	rule    = "0x45475a" // hairline between the two
	fg      = "0xcdd6f4"
	size    = 25
	pasteMS = 300 // VHS sends a paste as one burst, whatever its length
)

type cue struct {
	at   time.Duration
	text string
}

var (
	typingRe = regexp.MustCompile(`^Set TypingSpeed (\S+)$`)
	sleepRe  = regexp.MustCompile(`^Sleep (\S+)$`)
	typeRe   = regexp.MustCompile(`^Type(@\S+)? "(.*)"$`)
	outputRe = regexp.MustCompile(`(?m)^Output (\S+\.(gif|mp4))$`)
	keyRe    = regexp.MustCompile(`^(Enter|Down|Up|Left|Right|Tab|Escape|Backspace|Delete|Space|PageUp|PageDown|Ctrl\+\S+|Alt\+\S+)(\s+(\d+))?$`)
)

// dur parses VHS's duration spelling: bare numbers are seconds, `ms` is not.
func dur(s string) time.Duration {
	if strings.HasSuffix(s, "ms") {
		f, _ := strconv.ParseFloat(strings.TrimSuffix(s, "ms"), 64)
		return time.Duration(f * float64(time.Millisecond))
	}
	f, _ := strconv.ParseFloat(strings.TrimSuffix(s, "s"), 64)
	return time.Duration(f * float64(time.Second))
}

// parse walks the tape, returning the cues in tape order and the length the tape
// predicts for itself.
func parse(src string) ([]cue, time.Duration) {
	var cues []cue
	var t time.Duration
	typing := 50 * time.Millisecond
	recording := false

	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#>"):
			if recording {
				cues = append(cues, cue{t, strings.TrimSpace(line[2:])})
			}
			continue
		case strings.HasPrefix(line, "#"):
			continue
		case line == "Show":
			recording, t = true, 0
			continue
		case line == "Hide":
			recording = false
			continue
		case line == "Paste":
			t += pasteMS * time.Millisecond
			continue
		}
		if m := typingRe.FindStringSubmatch(line); m != nil {
			typing = dur(m[1])
		} else if m := sleepRe.FindStringSubmatch(line); m != nil {
			t += dur(m[1])
		} else if m := typeRe.FindStringSubmatch(line); m != nil {
			each := typing
			if m[1] != "" {
				each = dur(m[1][1:])
			}
			t += time.Duration(len([]rune(m[2]))) * each
		} else if m := keyRe.FindStringSubmatch(line); m != nil {
			n := 1
			if m[3] != "" {
				n, _ = strconv.Atoi(m[3])
			}
			t += time.Duration(n) * typing
		}
		// Output / Set / Require / Copy / Screenshot cost nothing worth counting.
	}
	return cues, t
}

func seconds(f string) (float64, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
		"format=duration", "-of", "csv=p=0", f).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe %s: %w", f, err)
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// chain is one drawtext per cue, each enabled over its own window. The text
// comes from a file rather than from the filter string: captions are full of the
// characters filter syntax reserves (: , ' →), and escaping them by hand is how
// a caption ends up silently truncated at its first colon.
func chain(cues []cue, end float64, work, font string) (string, error) {
	parts := []string{
		fmt.Sprintf("pad=iw:ih+%d:0:0:color=%s", strip, bg),
		fmt.Sprintf("drawbox=x=0:y=ih-%d:w=iw:h=1:color=%s:t=fill", strip, rule),
	}
	for i, c := range cues {
		if c.text == "" {
			continue // a bare #> clears the strip
		}
		file := filepath.Join(work, fmt.Sprintf("caption-%02d.txt", i))
		if err := os.WriteFile(file, []byte(c.text), 0o644); err != nil {
			return "", err
		}
		stop := end
		if i+1 < len(cues) {
			stop = cues[i+1].at.Seconds()
		}
		parts = append(parts, fmt.Sprintf(
			"drawtext=fontfile=%s:textfile=%s:fontsize=%d:fontcolor=%s"+
				":x=(w-text_w)/2:y=h-%d+(%d-text_h)/2:enable='between(t,%.2f,%.2f)'",
			font, file, size, fg, strip, strip, c.at.Seconds(), stop))
	}
	return strings.Join(parts, ","), nil
}

func ffmpeg(label string, args ...string) error {
	fmt.Printf("  %s…\n", label)
	cmd := exec.Command("ffmpeg", append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "caption: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	tapePath := "demo/snipe.tape"
	if len(os.Args) > 1 {
		tapePath = os.Args[1]
	}
	b, err := os.ReadFile(tapePath)
	if err != nil {
		return err
	}
	demo := filepath.Dir(tapePath)
	work := filepath.Join(demo, ".sandbox")

	font, err := exec.Command("fc-match", "-f", "%{file}", "JetBrainsMono Nerd Font:style=Regular").Output()
	if err != nil {
		return fmt.Errorf("fc-match: %w", err)
	}

	cues, predicted := parse(string(b))
	if predicted <= 0 {
		return fmt.Errorf("%s: nothing recorded — is there a Show?", tapePath)
	}

	// The tape already names its own files. Reading them back off it means the
	// two halves cannot disagree about which recording is being captioned:
	// .sandbox/raw-<name>.<ext> in, snipe-<name>.<ext> out.
	raw := map[string]string{}
	for _, m := range outputRe.FindAllStringSubmatch(string(b), -1) {
		raw[m[2]] = m[1]
	}
	out := func(ext string) string {
		return filepath.Join(demo, "snipe-"+strings.TrimPrefix(filepath.Base(raw[ext]), "raw-"))
	}
	// The mp4 is required: it is what the cue timeline is scaled against, and
	// it is what ships. A tape that wants a GIF as well declares one, and a tape
	// that does not is spared the encode — a three-minute GIF is a throwaway
	// several megabytes larger than the mp4 of the same session.
	if raw["mp4"] == "" {
		return fmt.Errorf("%s declares no mp4 Output", tapePath)
	}
	for ext, path := range raw {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("missing the %s recording — run: vhs %s", ext, tapePath)
		}
	}

	// The predicted timeline is uniformly short (VHS spends a little on every
	// keystroke beyond the typing delay), so stretch it onto the real duration
	// rather than trusting the model's absolute numbers.
	real, err := seconds(raw["mp4"])
	if err != nil {
		return err
	}
	scale := real / predicted.Seconds()
	for i := range cues {
		cues[i].at = time.Duration(float64(cues[i].at) * scale)
	}
	fmt.Printf("%d cues · predicted %.1fs · actual %.1fs · scale %.3f\n",
		len(cues), predicted.Seconds(), real, scale)

	filters, err := chain(cues, real, work, strings.TrimSpace(string(font)))
	if err != nil {
		return err
	}
	if err := ffmpeg("mp4", "-i", raw["mp4"], "-vf", filters,
		"-c:v", "libx264", "-crf", "20", "-pix_fmt", "yuv420p", out("mp4")); err != nil {
		return err
	}
	// The GIF is captioned from the GIF, not from the encoded mp4: the source is
	// already palettised, so the text stays as crisp as VHS drew the terminal.
	if raw["gif"] != "" {
		if err := ffmpeg("gif", "-i", raw["gif"], "-filter_complex",
			filters+",split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=3",
			"-loop", "0", out("gif")); err != nil {
			return err
		}
	}

	ents, _ := os.ReadDir(work)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "caption-") {
			_ = os.Remove(filepath.Join(work, e.Name()))
		}
	}
	for ext := range raw {
		if st, err := os.Stat(out(ext)); err == nil {
			fmt.Printf("  %s — %.1f MB\n", filepath.Base(out(ext)), float64(st.Size())/1e6)
		}
	}
	return nil
}
