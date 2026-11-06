package tui

import (
	"bytes"
	"io"
	"testing"
)

func run(t *testing.T, input string, h *History) (string, error) {
	t.Helper()
	data := []byte(input)
	i := 0
	next := func() (byte, bool) {
		if i >= len(data) {
			return 0, false
		}
		b := data[i]
		i++
		return b, true
	}
	var out bytes.Buffer
	return editLine(next, func() bool { return i < len(data) }, &out, "> ", h)
}

const up, down, left, right = "\x1b[A", "\x1b[B", "\x1b[D", "\x1b[C"

func TestUpArrowRecallsPreviousCommands(t *testing.T) {
	h := &History{Items: []string{"put a 1", "get a"}}
	if got, _ := run(t, up+"\r", h); got != "get a" {
		t.Fatalf("one up: %q", got)
	}
	if got, _ := run(t, up+up+"\r", h); got != "put a 1" {
		t.Fatalf("two up: %q", got)
	}
	if got, _ := run(t, up+up+up+"\r", h); got != "put a 1" {
		t.Fatalf("up past the start must stay on the oldest: %q", got)
	}
	if got, _ := run(t, up+up+down+"\r", h); got != "get a" {
		t.Fatalf("down: %q", got)
	}
	if got, _ := run(t, "par"+up+down+"\r", h); got != "par" {
		t.Fatalf("down past the newest must restore what was being typed: %q", got)
	}
}

func TestEditingKeys(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abc\r", "abc"},
		{"abc\x7f\r", "ab"},
		{"abc" + left + left + "X\r", "aXbc"},
		{"abc" + left + left + right + "X\r", "abXc"},
		{"abc\x01X\r", "Xabc"},
		{"abc\x01\x05X\r", "abcX"},
		{"abc def\x17X\r", "abc X"},
		{"abc def\x15X\r", "X"},
		{"abcdef" + left + left + left + "\x0b\r", "abc"},
		{"abc" + left + "\x1b[3~\r", "ab"},
		{"abc\x1b[H" + "X\r", "Xabc"},
		{"abc\x1b[H\x1b[F" + "X\r", "abcX"},
		{"héllo ✓\r", "héllo ✓"},
		{"é" + left + "x\r", "xé"},
	}
	for _, c := range cases {
		if got, err := run(t, c.in, nil); err != nil || got != c.want {
			t.Errorf("%q -> %q (%v), want %q", c.in, got, err, c.want)
		}
	}
}

func TestInterruptAndEOF(t *testing.T) {
	if _, err := run(t, "abc\x03", nil); err != ErrInterrupt {
		t.Fatalf("Ctrl-C: %v", err)
	}
	if _, err := run(t, "\x04", nil); err != io.EOF {
		t.Fatalf("Ctrl-D on an empty line: %v", err)
	}
	if got, err := run(t, "ab\x04\r", nil); err != nil || got != "ab" {
		t.Fatalf("Ctrl-D on a non-empty line deletes: %q %v", got, err)
	}
}

func TestHistoryAddSkipsEmptyAndRepeats(t *testing.T) {
	h := &History{}
	h.Add("")
	h.Add("a")
	h.Add("a")
	h.Add("b")
	if len(h.Items) != 2 {
		t.Fatalf("%v", h.Items)
	}
}
