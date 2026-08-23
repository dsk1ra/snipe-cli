package tui

import (
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"
)

// renderReport lays a report out for the reader pane at width w: markdown tables
// reflowed into columns that fit, every other line word-wrapped.
//
// It is deliberately not a markdown renderer. Across 300 reports the pipeline has
// emitted five constructs — tables, headings, bold leads, bullets and rules — and
// not one code fence, link or blockquote, because the reports come from a
// template rather than from a person. Tables are 43% of the lines and carry the
// reasoning in cells running past 900 characters, and they are the one construct
// word-wrapping alone makes WORSE: wrap a row and its columns fold into each
// other. The other four read as their own source.
//
// The alternative was glamour, which renders those tables just as well and costs
// fifteen further modules including a second major version of lipgloss. It also
// folds the "**Date:** … **Score:** …" header block into one run-on paragraph —
// correct by CommonMark, since those lines are separated by single newlines, and
// wrong for this document.
func renderReport(src string, w int) string {
	if w < 1 {
		return src
	}
	var out, block []string
	flush := func() {
		if len(block) > 0 {
			out = append(out, renderTable(block, w))
			block = nil
		}
	}
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			block = append(block, line)
			continue
		}
		flush()
		out = append(out, wrapText(line, w))
	}
	flush()
	return strings.Join(out, "\n")
}

// wrapText word-wraps to w and then holds it there.
//
// ansi.Wrap alone is the right shape — it breaks a word rather than let it
// overhang, which is what keeps a 1,000-character tracking URL inside the pane —
// but it lands a column or two PAST the limit when it breaks on a hyphen, and
// the viewport cuts anything over. Hardwrap after it costs an orphaned hyphen
// once in every fifty thousand lines; measured over every report on disk at five
// widths, this pair was the only one that never overran, and Wordwrap in its
// place orphans two hundred times as often because it will not break the long
// token itself and leaves Hardwrap to chop it blind.
func wrapText(s string, w int) string {
	return ansi.Hardwrap(ansi.Wrap(s, w, ""), w, false)
}

const (
	// colChrome is what a column costs before any text: one border and two of
	// padding.
	colChrome = 3
	// minTextW is the narrowest a column of prose can be and still hold words
	// rather than fragments of them.
	minTextW = 12
)

// fitsColumns reports whether w can give every column its header and its median
// cell.
//
// The median is the width lipgloss shrinks a column toward, so this asks the
// question the renderer is about to answer, rather than guessing from the column
// count. Measured over every table on disk it splits them where you would by
// eye: all 600 two-column tables fit, none of the 595 five- and seven-column
// ones do, and the three-column tables divide almost evenly between the small
// summaries and the CV-match matrices whose evidence cells run to a paragraph.
//
// Guessing from the count instead left the CV-match table in columns, where
// lipgloss squeezed its verdict column under its own content and broke
// "Transferable" across two lines — on the table that appears in every report.
func fitsColumns(cells [][]string, w int) bool {
	need := 0
	for i := range cells[0] {
		widths := make([]int, 0, len(cells))
		for _, r := range cells {
			widths = append(widths, lipgloss.Width(r[i]))
		}
		sort.Ints(widths)
		need += max(lipgloss.Width(cells[0][i]), widths[len(widths)/2]) + colChrome
	}
	return need <= w
}

// renderTable reflows one run of pipe-delimited rows to fit w.
//
// Only the column rules are drawn, not a box: the pane already has a frame, and
// a second one inside it reads as a nested window rather than as a table.
func renderTable(rows []string, w int) string {
	cells := make([][]string, 0, len(rows))
	cols := 0
	for _, r := range rows {
		c := splitRow(r)
		if isRule(c) {
			// The |---|---| under the header carries no data; the header style
			// is what separates them here.
			continue
		}
		cells = append(cells, c)
		cols = max(cols, len(c))
	}
	if len(cells) == 0 {
		return ""
	}
	// Ragged rows are squared here rather than at the renderer, which drops the
	// cells of a row longer than its header instead of widening the table.
	for i, c := range cells {
		for len(c) < cols {
			c = append(c, "")
		}
		cells[i] = c[:cols]
	}
	if !fitsColumns(cells, w) {
		return wideTable(cells, w)
	}

	pad := lipgloss.NewStyle().Padding(0, 1)
	return table.New().Width(w).Wrap(true).
		Border(lipgloss.NormalBorder()).
		BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).
		BorderRow(false).BorderColumn(true).
		BorderStyle(styDim).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return pad.Bold(true)
			}
			return pad
		}).
		Headers(cells[0]...).Rows(cells[1:]...).Render()
}

// wideTable lays a table out one record per block rather than one per line: the
// first cell as the record's heading, then a labelled line for each of the rest.
//
// This is where the STAR matrices go. Seven columns share 102 between them at
// fourteen characters each, and a paragraph of evidence down a fourteen-column
// ribbon runs forty lines with six empty ones beside it. Turned on its side the
// same record reads as prose with its labels down the left, and the width stops
// mattering. Just over a quarter of the table rows on disk are five or seven
// columns wide, so this is not the rare path.
func wideTable(cells [][]string, w int) string {
	head := cells[0]
	labelW := 0
	for _, h := range head[1:] {
		labelW = max(labelW, lipgloss.Width(h))
	}
	// Two of indent, the label, and one space before the value. Where that
	// leaves the value no room to hold words, the label goes on its own line
	// instead — the pane is then narrower than a phone, and a column of labels
	// beside a column of single words is not a saving.
	valW, stacked := w-labelW-3, false
	if valW < minTextW {
		valW, stacked = max(1, w-2), true
	}
	var b strings.Builder
	for _, row := range cells[1:] {
		b.WriteString(styHeading.Render(wrapText(strings.TrimSpace(head[0]+" "+row[0]), w)) + "\n")
		for i := 1; i < len(row); i++ {
			if row[i] == "" {
				continue
			}
			if stacked {
				b.WriteString("  " + styDim.Render(truncate(head[i], valW)) + "\n")
			}
			for j, l := range strings.Split(wrapText(row[i], valW), "\n") {
				switch {
				case stacked:
					b.WriteString("  " + l + "\n")
				case j == 0:
					b.WriteString("  " + styDim.Render(pad(head[i], labelW)) + " " + l + "\n")
				default:
					b.WriteString("  " + strings.Repeat(" ", labelW) + " " + l + "\n")
				}
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// splitRow breaks "| a | b |" into its trimmed cells. No report has ever carried
// an escaped pipe, so the split needs no parser.
func splitRow(line string) []string {
	out := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return out
}

// isRule reports whether a row is the |---|:--:| divider under a header.
func isRule(cells []string) bool {
	for _, c := range cells {
		if c == "" || strings.Trim(c, ":-") != "" {
			return false
		}
	}
	return len(cells) > 0
}
