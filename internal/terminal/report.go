// Package terminal renders the short-lived progress line and the final CLI report.
package terminal

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Kevin-Ebrahim/gh-tune/internal/setup"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/rivo/uniseg"
)

type Display struct {
	out        io.Writer
	width      int
	tty, color bool
}

func New(out io.Writer) *Display {
	d := &Display{out: out, width: 80}
	f, ok := out.(*os.File)
	if !ok || !term.IsTerminal(f) || os.Getenv("TERM") == "dumb" {
		return d
	}
	t := term.FromEnv()
	d.tty = true
	d.color = t.IsColorEnabled() && !term.IsColorDisabled()
	if width, _, err := t.Size(); err == nil && width > 0 {
		d.width = width
	}
	return d
}

func (d *Display) paint(code, text string) string {
	if !d.color {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (d *Display) Header(repo, profile, mode string) {
	fmt.Fprintln(d.out, d.paint("1", repo))
	fmt.Fprintf(d.out, "Target profile  %s   ·   %s\n", d.paint("1;36", plain(profile)), mode)
}

func (d *Display) Report(rows []setup.Result, mode string) {
	if len(rows) == 0 {
		fmt.Fprintln(d.out, "\nThis profile manages no settings.")
		return
	}
	planned := "Planned changes"
	if mode == "Check" {
		planned = "Differences from profile"
	}
	sections := []struct{ status, title, color string }{
		{"BLOCKED", "Needs attention", "1;31"}, {"APPLIED", "Applied and verified", "1;32"},
		{"CHANGE", planned, "1;33"}, {"AFTER", "After prerequisites", "1;33"},
		{"PENDING", "Awaiting PR review", "1;36"}, {"MATCH", "Matching settings", "1;32"}, {"N/A", "Not applicable", "1"},
	}
	labelWidth := 0
	for _, r := range rows {
		labelWidth = max(labelWidth, uniseg.StringWidth(r.Setting))
	}
	for _, s := range sections {
		var group []setup.Result
		for _, r := range rows {
			if r.Status == s.status {
				group = append(group, r)
			}
		}
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(d.out, "\n%s  (%d)\n", d.paint(s.color, s.title), len(group))
		for _, r := range group {
			d.row(r, labelWidth)
		}
	}
}

func (d *Display) row(r setup.Result, labelWidth int) {
	name := initialUpper(r.Setting)
	first, extra, _ := strings.Cut(r.Detail, "\n")
	indent := strings.Repeat(" ", labelWidth+4)
	if d.width-len(indent) < 24 {
		fmt.Fprintf(d.out, "  %s\n", name)
		indent = "    "
		fmt.Fprint(d.out, indent)
	} else {
		fmt.Fprintf(d.out, "  %s%s", name, strings.Repeat(" ", labelWidth-uniseg.StringWidth(name)+2))
	}
	for i, line := range wrap(plain(first), d.width-len(indent)) {
		if i > 0 {
			fmt.Fprint(d.out, indent)
		}
		if before, after, ok := strings.Cut(line, " → "); ok && r.Status == "CHANGE" {
			line = before + " → " + d.paint("1;33", after)
		}
		fmt.Fprintln(d.out, line)
	}
	if extra != "" {
		// Keep file previews verbatim, including meaningful whitespace within added lines.
		for _, line := range strings.Split(extra, "\n") {
			fmt.Fprintf(d.out, "    %s\n", plain(line))
		}
	}
}

func initialUpper(s string) string {
	if s == "" || strings.HasPrefix(s, "gh-tune") {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// Remove terminal control characters in data returned by GitHub or read from flags.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\t' {
			return -1
		}
		return r
	}, s)
}

func wrap(s string, width int) []string {
	if uniseg.StringWidth(s) <= width {
		return []string{s}
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line != "" && uniseg.StringWidth(line+" "+word) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	return append(lines, line)
}
