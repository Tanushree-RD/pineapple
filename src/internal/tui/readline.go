package tui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ErrInterrupt is returned by ReadLine when the user presses Ctrl-C at the prompt.
var ErrInterrupt = errors.New("interrupted")

// History is the list of lines typed at a prompt, newest last, optionally kept in a file between sessions.
type History struct {
	Items []string
	path  string
}

// LoadHistory reads the history file of a named prompt from the user cache directory (missing is fine).
func LoadHistory(name string) *History {
	h := &History{}
	base, err := os.UserCacheDir()
	if err != nil {
		return h
	}
	h.path = filepath.Join(base, "hydra-ui", "history-"+name)
	if b, err := os.ReadFile(h.path); err == nil {
		for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			if l != "" {
				h.Items = append(h.Items, l)
			}
		}
	}
	if len(h.Items) > 500 {
		h.Items = h.Items[len(h.Items)-500:]
	}
	return h
}

// Add remembers a line (not an empty one, not a repeat of the last) and appends it to the file.
func (h *History) Add(line string) {
	if h == nil || line == "" || (len(h.Items) > 0 && h.Items[len(h.Items)-1] == line) {
		return
	}
	h.Items = append(h.Items, line)
	if h.path != "" {
		_ = os.MkdirAll(filepath.Dir(h.path), 0o755)
		if f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, line)
			f.Close()
		}
	}
}

// ReadLine prints prompt and reads one line. In a terminal it is a small editor: up/down walk the history,
// left/right/home/end move, backspace/delete edit, Ctrl-A/E/U/K/W as in a shell, Ctrl-C cancels the line
// (ErrInterrupt) and Ctrl-D on an empty line ends input (io.EOF). Anywhere else it is a plain line read.
// The terminal is put back in normal mode before it returns, so a program started with the line behaves normally.
func ReadLine(out io.Writer, in *bufio.Reader, prompt string, h *History) (string, error) {
	restore, raw := EnterRaw()
	if !raw || !IsTTY() {
		fmt.Fprint(out, prompt)
		l, err := in.ReadString('\n')
		return strings.TrimRight(l, "\r\n"), err
	}
	defer restore()
	line, err := editLine(nextByte, func() bool { return len(pending) > 0 }, out, prompt, h)
	if err == nil {
		h.Add(strings.TrimSpace(line))
	}
	return line, err
}

// editLine is the editor itself, driven by a byte source so it can be tested without a terminal.
func editLine(next func() (byte, bool), ready func() bool, out io.Writer, prompt string, h *History) (string, error) {
	var buf []rune
	pos := 0
	var items []string
	if h != nil {
		items = h.Items
	}
	hist := len(items) // index into items; len(items) is the line being typed
	var draft string
	paint := func() {
		fmt.Fprint(out, "\r"+prompt+string(buf)+"\x1b[K")
		if back := len(buf) - pos; back > 0 {
			fmt.Fprintf(out, "\x1b[%dD", back)
		}
	}
	set := func(s string) { buf = []rune(s); pos = len(buf) }
	paint()
	for {
		b, ok := next()
		if !ok {
			return "", io.EOF
		}
		switch {
		case b == '\r' || b == '\n':
			fmt.Fprint(out, "\r\n")
			return string(buf), nil
		case b == 3: // Ctrl-C
			fmt.Fprint(out, "^C\r\n")
			return "", ErrInterrupt
		case b == 4: // Ctrl-D
			if len(buf) == 0 {
				fmt.Fprint(out, "\r\n")
				return "", io.EOF
			}
			if pos < len(buf) {
				buf = append(buf[:pos], buf[pos+1:]...)
			}
		case b == 1: // Ctrl-A
			pos = 0
		case b == 5: // Ctrl-E
			pos = len(buf)
		case b == 21: // Ctrl-U
			buf, pos = buf[pos:], 0
		case b == 11: // Ctrl-K
			buf = buf[:pos]
		case b == 23: // Ctrl-W: delete the word before the cursor
			i := pos
			for i > 0 && buf[i-1] == ' ' {
				i--
			}
			for i > 0 && buf[i-1] != ' ' {
				i--
			}
			buf = append(buf[:i], buf[pos:]...)
			pos = i
		case b == 12: // Ctrl-L
			fmt.Fprint(out, "\x1b[2J\x1b[H")
		case b == 127 || b == 8: // backspace
			if pos > 0 {
				buf = append(buf[:pos-1], buf[pos:]...)
				pos--
			}
		case b == 0x1b:
			seq := []byte{}
			for len(seq) < 4 {
				if !ready() {
					break
				}
				c, ok := next()
				if !ok {
					break
				}
				seq = append(seq, c)
				if (c >= 'A' && c <= 'Z') || c == '~' {
					break
				}
			}
			switch string(seq) {
			case "[A", "OA": // up
				if hist > 0 {
					if hist == len(items) {
						draft = string(buf)
					}
					hist--
					set(items[hist])
				}
			case "[B", "OB": // down
				if hist < len(items) {
					hist++
					if hist == len(items) {
						set(draft)
					} else {
						set(items[hist])
					}
				}
			case "[C", "OC":
				if pos < len(buf) {
					pos++
				}
			case "[D", "OD":
				if pos > 0 {
					pos--
				}
			case "[H", "OH", "[1~", "[7~":
				pos = 0
			case "[F", "OF", "[4~", "[8~":
				pos = len(buf)
			case "[3~": // delete
				if pos < len(buf) {
					buf = append(buf[:pos], buf[pos+1:]...)
				}
			}
		case b >= 0x20 && b != 0x7f:
			// a printable character; gather the rest of a multi-byte UTF-8 rune
			bs := []byte{b}
			for need := utf8Len(b) - 1; need > 0; need-- {
				c, ok := next()
				if !ok {
					break
				}
				bs = append(bs, c)
			}
			r, _ := utf8.DecodeRune(bs)
			buf = append(buf, 0)
			copy(buf[pos+1:], buf[pos:])
			buf[pos] = r
			pos++
		}
		paint()
	}
}

func utf8Len(b byte) int {
	switch {
	case b >= 0xf0:
		return 4
	case b >= 0xe0:
		return 3
	case b >= 0xc0:
		return 2
	}
	return 1
}
