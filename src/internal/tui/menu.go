package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Key is a decoded key press.
type Key int

const (
	KeyOther Key = iota
	KeyUp
	KeyDown
	KeyEnter
	KeyEsc
	KeyCtrlC
	KeyQuit
)

// EnterRaw switches the terminal to unbuffered, no-echo input using stty (Linux and macOS) and
// returns a function that restores it. ok is false when stdin is not a terminal or stty fails.
func EnterRaw() (restore func(), ok bool) {
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return func() {}, false
	}
	get := exec.Command("stty", "-g")
	get.Stdin = os.Stdin
	saved, err := get.Output()
	if err != nil {
		return func() {}, false
	}
	set := exec.Command("stty", "-icanon", "-echo", "min", "1", "time", "0")
	set.Stdin = os.Stdin
	if err := set.Run(); err != nil {
		return func() {}, false
	}
	state := strings.TrimSpace(string(saved))
	return func() {
		r := exec.Command("stty", state)
		r.Stdin = os.Stdin
		_ = r.Run()
	}, true
}

var pending []byte

// nextByte returns the next input byte, reading from stdin when the buffer is empty. Keys that arrive
// together (fast typing, paste) are kept for later calls instead of being dropped.
func nextByte() (byte, bool) {
	if len(pending) == 0 {
		buf := make([]byte, 64)
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return 0, false
		}
		pending = buf[:n]
	}
	b := pending[0]
	pending = pending[1:]
	return b, true
}

// ReadKey blocks for one key press (the terminal must be in raw mode).
func ReadKey() Key {
	b, ok := nextByte()
	if !ok {
		return KeyCtrlC
	}
	switch b {
	case 0x1b:
		if len(pending) >= 2 && pending[0] == '[' {
			c := pending[1]
			pending = pending[2:]
			switch c {
			case 'A':
				return KeyUp
			case 'B':
				return KeyDown
			}
			return KeyOther
		}
		return KeyEsc
	case '\r', '\n':
		return KeyEnter
	case 3:
		return KeyCtrlC
	case 'k':
		return KeyUp
	case 'j':
		return KeyDown
	case 'q':
		return KeyQuit
	}
	return KeyOther
}

// MenuItem is one row of a menu.
type MenuItem struct {
	Label string
	Hint  string
}

func renderMenu(items []MenuItem, sel, w int) []string {
	var lines []string
	labelW := 0
	for _, it := range items {
		if n := VisLen(it.Label); n > labelW {
			labelW = n
		}
	}
	for i, it := range items {
		hint := Trunc(it.Hint, w-labelW-8)
		if i == sel {
			lines = append(lines, Cyan("  ❯ ")+Bold(PadRight(it.Label, labelW))+"  "+Gray(hint))
		} else {
			lines = append(lines, "    "+PadRight(it.Label, labelW)+"  "+Dim(hint))
		}
	}
	return lines
}

// Menu shows items and returns the chosen index; ok is false if the user quit. In a terminal it is an
// arrow-key list (up/down or j/k, enter, q/esc to quit); otherwise it prints a numbered list and reads a number.
func Menu(out io.Writer, in *bufio.Reader, title string, items []MenuItem) (idx int, ok bool) {
	if title != "" {
		fmt.Fprintln(out, title)
	}
	restore, raw := EnterRaw()
	if !raw || !IsTTY() {
		for i, it := range items {
			fmt.Fprintf(out, "  %2d) %s  %s\n", i+1, it.Label, Strip(it.Hint))
		}
		fmt.Fprint(out, "choose a number (empty to quit): ")
		line, _ := in.ReadString('\n')
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || n < 1 || n > len(items) {
			return 0, false
		}
		return n - 1, true
	}
	defer restore()
	HideCursor(out)
	defer ShowCursor(out)
	w := Width()
	sel, height := 0, 0
	for {
		height = Redraw(out, height, renderMenu(items, sel, w))
		fmt.Fprint(out, Dim("   ↑/↓ move · enter select · q quit")+"\r")
		switch ReadKey() {
		case KeyUp:
			sel = (sel + len(items) - 1) % len(items)
		case KeyDown:
			sel = (sel + 1) % len(items)
		case KeyEnter:
			ClearLine(out)
			return sel, true
		case KeyEsc, KeyQuit, KeyCtrlC:
			ClearLine(out)
			return 0, false
		}
		ClearLine(out)
	}
}
