package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chethan-9817/pineapple/src/internal/tui"
)

func newApp(out *bytes.Buffer) *app {
	tui.SetColor(false)
	return &app{out: out, in: bufio.NewReader(strings.NewReader("")), hist: &tui.History{}}
}

func TestKvdbNeedsADatabaseUntilOneIsChosen(t *testing.T) {
	var out bytes.Buffer
	a := newApp(&out)
	if a.exec("put k v") {
		t.Fatal("must not quit")
	}
	if !strings.Contains(out.String(), "name the database") {
		t.Fatalf("%q", out.String())
	}
}

func TestLooksLikePath(t *testing.T) {
	if !looksLikePath("x.db") || looksLikePath("student:101") {
		t.Fatal("path detection wrong")
	}
	f := filepath.Join(t.TempDir(), "data")
	os.WriteFile(f, []byte("x"), 0o644)
	if !looksLikePath(f) {
		t.Fatal("an existing file is a path")
	}
}

func TestWordsAreRoutedAndUnknownIsAnswered(t *testing.T) {
	var out bytes.Buffer
	a := newApp(&out)
	if !a.exec("/exit") {
		t.Fatal("/exit must leave")
	}
	out.Reset()
	a.exec("frobnicate")
	if !strings.Contains(out.String(), "unknown command") {
		t.Fatal("an unknown word must be answered")
	}
	out.Reset()
	a.exec("/hydra")
	if !strings.Contains(out.String(), "no need to switch") {
		t.Fatalf("%q", out.String())
	}
	for _, w := range []string{"put", "get", "dump", "check", "open", "serve", "batch", "del"} {
		if !kvdbCommands[w] {
			t.Errorf("kvdb word %s missing", w)
		}
	}
	for _, w := range []string{"attack", "judge", "scenario", "compaction", "recovery-crash"} {
		if !hydraWords[w] {
			t.Errorf("hydra word %s missing", w)
		}
	}
}
