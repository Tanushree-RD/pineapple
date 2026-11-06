package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/chethan-9817/pineapple/src/internal/tui"
)

func TestSlashCommandsSwitchEnvironments(t *testing.T) {
	tui.SetColor(false)
	var got [][2]string
	sw := func(name, db string) error { got = append(got, [2]string{name, db}); return nil }
	var out bytes.Buffer
	if code := run(nil, &out, bufio.NewReader(strings.NewReader("nonsense\n/hydra students.db\n")), sw); code != 0 {
		t.Fatal(code)
	}
	if len(got) != 1 || got[0] != [2]string{"hydra", "students.db"} {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(out.String(), "unknown") {
		t.Fatal("an unknown word must be answered, not ignored")
	}
	got = nil
	if code := run([]string{"/leapfrog"}, &out, nil, sw); code != 0 || len(got) != 1 || got[0][0] != "leapfrog" {
		t.Fatalf("%d %v", code, got)
	}
}
