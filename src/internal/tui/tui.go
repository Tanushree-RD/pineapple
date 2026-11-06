// Package tui is a tiny terminal toolkit for the LEAPFROG and HYDRA command-line tools:
// colors, boxes, progress bars, sparklines and an arrow-key menu.
//
// It uses only the Go standard library, like the rest of the project: ANSI escape codes for
// drawing and `stty` for raw key input. Everything degrades to plain text when the output is not
// a terminal, when NO_COLOR is set, or when TERM is "dumb", so output stays safe to pipe and log.
//
// Looks borrowed (not copied) from tools we like: the rounded boxes and gradient banners of
// Charm's gum, the arrow-key lists of lazygit and k9s, the progress bars of cargo, the sparklines
// of htop-style monitors.
package tui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var color = detectColor()

func detectColor() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return IsTTY()
}

// SetColor forces colors on or off (used by tests and --no-color flags).
func SetColor(on bool) { color = on }

// ColorEnabled reports whether styled output is active.
func ColorEnabled() bool { return color }

// IsTTY reports whether standard output is an interactive terminal.
func IsTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func paint(code, s string) string {
	if !color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// Basic styles.
func Bold(s string) string    { return paint("1", s) }
func Dim(s string) string     { return paint("2", s) }
func Italic(s string) string  { return paint("3", s) }
func Red(s string) string     { return paint("38;5;203", s) }
func Green(s string) string   { return paint("38;5;78", s) }
func Yellow(s string) string  { return paint("38;5;221", s) }
func Blue(s string) string    { return paint("38;5;75", s) }
func Magenta(s string) string { return paint("38;5;177", s) }
func Cyan(s string) string    { return paint("38;5;87", s) }
func Gray(s string) string    { return paint("38;5;245", s) }

// C paints s with a 256-color foreground.
func C(n int, s string) string { return paint("38;5;"+strconv.Itoa(n), s) }

// Reverse paints s in inverse video (used for the selected menu row).
func Reverse(s string) string { return paint("7", s) }

// Gradient colors each rune of s along a 256-color ramp from `from` to `to`.
func Gradient(s string, from, to int) string {
	if !color {
		return s
	}
	runes := []rune(s)
	var sb strings.Builder
	for i, r := range runes {
		n := from
		if len(runes) > 1 {
			n = from + (to-from)*i/(len(runes)-1)
		}
		sb.WriteString("\x1b[38;5;" + strconv.Itoa(n) + "m" + string(r))
	}
	sb.WriteString("\x1b[0m")
	return sb.String()
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// Strip removes ANSI escape sequences.
func Strip(s string) string { return ansiRE.ReplaceAllString(s, "") }

// VisLen is the number of visible characters in s (escape codes do not count).
func VisLen(s string) int { return utf8.RuneCountInString(Strip(s)) }

// PadRight pads s with spaces to visible width w.
func PadRight(s string, w int) string {
	if n := VisLen(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// Trunc shortens plain text to w runes, adding an ellipsis. Call it before styling.
func Trunc(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w <= 1 {
		return string(r[:w])
	}
	return string(r[:w-1]) + "…"
}

// Width is the terminal width, clamped to [60, 110]; 80 when unknown.
func Width() int {
	w := 0
	if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil {
		w = c
	}
	if w == 0 && IsTTY() {
		cmd := exec.Command("stty", "size")
		cmd.Stdin = os.Stdin
		if out, err := cmd.Output(); err == nil {
			if f := strings.Fields(string(out)); len(f) == 2 {
				w, _ = strconv.Atoi(f[1])
			}
		}
	}
	if w == 0 {
		w = 80
	}
	if w < 60 {
		w = 60
	}
	if w > 110 {
		w = 110
	}
	return w
}

// Box draws a rounded box of total visible width w around lines, with an optional title in the top edge.
func Box(title string, lines []string, w int, border func(string) string) []string {
	if border == nil {
		border = Gray
	}
	inner := w - 4
	top := "╭" + strings.Repeat("─", w-2) + "╮"
	if title != "" {
		t := " " + title + " "
		rest := w - 2 - 1 - VisLen(t)
		if rest < 0 {
			rest = 0
		}
		top = border("╭─") + t + border(strings.Repeat("─", rest)+"╮")
	} else {
		top = border(top)
	}
	out := []string{top}
	for _, l := range lines {
		if VisLen(l) > inner {
			l = Trunc(Strip(l), inner)
		}
		out = append(out, border("│")+" "+PadRight(l, inner)+" "+border("│"))
	}
	out = append(out, border("╰"+strings.Repeat("─", w-2)+"╯"))
	return out
}

var partials = []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}

// Bar draws a progress bar of visible width w for frac in [0,1], shaded from green to amber as it fills.
func Bar(frac float64, w int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	total := frac * float64(w)
	full := int(total)
	part := int((total - float64(full)) * 8)
	var sb strings.Builder
	for i := 0; i < full; i++ {
		sb.WriteString(paint("38;5;"+strconv.Itoa(rampColor(i, w)), "█"))
	}
	used := full
	if full < w && part > 0 {
		sb.WriteString(paint("38;5;"+strconv.Itoa(rampColor(full, w)), partials[part]))
		used++
	}
	if used < w {
		sb.WriteString(paint("38;5;238", strings.Repeat("░", w-used)))
	}
	return sb.String()
}

// rampColor walks a green-to-teal ramp across the bar.
func rampColor(i, w int) int {
	ramp := []int{35, 41, 42, 43, 49, 50, 51}
	if w <= 1 {
		return ramp[0]
	}
	return ramp[i*(len(ramp)-1)/(w-1)]
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// Spark draws the last w values as a sparkline scaled to their own min and max.
func Spark(vals []float64, w int) string {
	if len(vals) > w {
		vals = vals[len(vals)-w:]
	}
	if len(vals) == 0 {
		return strings.Repeat(" ", w)
	}
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	var sb strings.Builder
	for _, v := range vals {
		idx := 0
		if hi > lo {
			idx = int((v - lo) / (hi - lo) * float64(len(sparkRunes)-1))
		} else {
			idx = len(sparkRunes) / 2
		}
		sb.WriteRune(sparkRunes[idx])
	}
	s := sb.String()
	if n := len(vals); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return Cyan(s)
}

// Dur formats a duration as m:ss (or h:mm:ss).
func Dur(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 0 {
		s = 0
	}
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// Bytes formats a byte count (1536 -> "1.5 KiB").
func Bytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for m := n / u; m >= u; m /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// Cursor and screen control (no-ops when not a terminal).
func CursorUp(w io.Writer, n int) {
	if IsTTY() && n > 0 {
		fmt.Fprintf(w, "\x1b[%dA", n)
	}
}
func ClearLine(w io.Writer) {
	if IsTTY() {
		fmt.Fprint(w, "\r\x1b[2K")
	}
}
func HideCursor(w io.Writer) {
	if IsTTY() {
		fmt.Fprint(w, "\x1b[?25l")
	}
}
func ShowCursor(w io.Writer) {
	if IsTTY() {
		fmt.Fprint(w, "\x1b[?25h")
	}
}

// Redraw rewrites a block of lines in place: it moves up over the previous block (prev lines),
// clears and prints the new one, and returns its height for the next call.
func Redraw(w io.Writer, prev int, lines []string) int {
	CursorUp(w, prev)
	for _, l := range lines {
		ClearLine(w)
		fmt.Fprintln(w, l)
	}
	// If the new block is shorter, blank the leftover lines.
	for i := len(lines); i < prev; i++ {
		ClearLine(w)
		fmt.Fprintln(w)
	}
	if prev > len(lines) {
		CursorUp(w, prev-len(lines))
	}
	return len(lines)
}

// Spinner frames.
var SpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Status symbols.
func OK() string   { return Green("✔") }
func Fail() string { return Red("✘") }
func Warn() string { return Yellow("!") }
