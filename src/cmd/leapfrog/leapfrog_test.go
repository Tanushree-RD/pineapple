package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chethan-9817/pineapple/src/engine"
	"github.com/chethan-9817/pineapple/src/internal/tui"
)

func newDB(t *testing.T, kv ...string) string {
	t.Helper()
	tui.SetColor(false)
	p := filepath.Join(t.TempDir(), "t.db")
	db, err := engine.Open(p, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if err := db.Put([]byte(kv[i]), []byte(kv[i+1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInspectDecodesARealFile(t *testing.T) {
	p := newDB(t, "a", "1", "b", "2")
	im, err := readImage(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(im.frames) != 2 || im.tail != "COMPLETE" || !im.slots[0].valid || im.slots[1].valid || im.chosen != 0 {
		t.Fatalf("%+v", im)
	}
	if im.frames[0].seq != 1 || im.frames[1].seq != 2 || !im.frames[0].crcOK || !im.frames[1].endOK {
		t.Fatalf("%+v", im.frames)
	}
	if !strings.Contains(im.frames[0].summary, "put a") || !strings.Contains(im.frames[1].summary, "put b") {
		t.Fatalf("summaries: %q %q", im.frames[0].summary, im.frames[1].summary)
	}
}

func TestInspectTailStates(t *testing.T) {
	p := newDB(t, "a", "1", "b", "2")
	st, _ := os.Stat(p)
	// Cut 3 bytes of the last end marker: SHORT_SENTINEL, the frame still counts (F4).
	if err := os.Truncate(p, st.Size()-3); err != nil {
		t.Fatal(err)
	}
	im, err := readImage(p)
	if err != nil || im.tail != "SHORT_SENTINEL" || len(im.frames) != 2 || im.tailBytes != 3 {
		t.Fatalf("short sentinel: %+v %v", im, err)
	}
	// Cut deep into the last frame: TORN, the frame is ignored.
	if err := os.Truncate(p, st.Size()-30); err != nil {
		t.Fatal(err)
	}
	im, err = readImage(p)
	if err != nil || im.tail != "TORN" || len(im.frames) != 1 {
		t.Fatalf("torn: %+v %v", im, err)
	}
}

func TestInspectRejectsAFileThatIsNotADatabase(t *testing.T) {
	p := filepath.Join(t.TempDir(), "junk")
	if err := os.WriteFile(p, bytes.Repeat([]byte{0x41}, 20000), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readImage(p); err == nil {
		t.Fatal("junk accepted")
	}
	var out bytes.Buffer
	if code := run([]string{"inspect", p}, &out, bufio.NewReader(strings.NewReader(""))); code != 1 || !strings.Contains(out.String(), "not a LEAPFROG-R database") {
		t.Fatalf("code %d: %s", code, out.String())
	}
}

func TestInspectOutput(t *testing.T) {
	p := newDB(t, "a", "1")
	var out bytes.Buffer
	if code := run([]string{"inspect", p, "-v"}, &out, bufio.NewReader(strings.NewReader(""))); code != 0 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"superblocks", "ACTIVE", "frames", "BATCH", "tail", "COMPLETE", "anatomy of frame #1", "magic", "end_magic", "LF-END!!"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("inspect output lacks %q:\n%s", want, out.String())
		}
	}
	for i, l := range strings.Split(out.String(), "\n") {
		if tui.VisLen(l) > 110 {
			t.Errorf("line %d is %d columns wide", i, tui.VisLen(l))
		}
	}
}

func TestArgDecodingAndEscaping(t *testing.T) {
	if b, err := decodeArg("hex:00ff"); err != nil || !bytes.Equal(b, []byte{0, 0xff}) {
		t.Fatal(b, err)
	}
	if _, err := decodeArg("hex:zz"); err == nil {
		t.Fatal("bad hex accepted")
	}
	cases := map[string]string{"plain": "plain", "": "hex:", "NOTFOUND": "hex:4e4f54464f554e44", "hex:x": "hex:6865783a78", "a\tb": "hex:610962", "a b": "a b"}
	for in, want := range cases {
		if got := esc([]byte(in)); got != want {
			t.Errorf("esc(%q) = %q, want %q", in, got, want)
		}
	}
}

// session feeds a script to the shell and returns everything it printed.
func session(t *testing.T, path, script string) string {
	t.Helper()
	tui.SetColor(false)
	var out bytes.Buffer
	in := bufio.NewReader(strings.NewReader(script))
	s, err := openShell(path, 0, false, &out, in, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.db.Close() }()
	s.run()
	return out.String()
}

func TestShellScriptedSession(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.db")
	out := session(t, p, strings.Join([]string{
		"put city Mumbai", "get city", "begin", "put a 1", "put b 2", "del city", "commit",
		"get city", "dump", "count", "put", "get", "nonsense", "put bin hex:00ff", "get bin",
		"del a", "count", "fill 120 50", "stats", "compact", "check", "inspect", "cap 4194304", "stats", "help", "quit",
	}, "\n"))
	for _, want := range []string{
		"stored", "Mumbai", "batch started", "queued put a (1 op in batch)", "queued put b (2 ops in batch)",
		"committed 3 ops atomically", "NOTFOUND", "ascending byte order", "usage: put", "usage: get", "unknown command",
		"hex:00ff", "deleted", "wrote 120 keys", "compacted", "slot A", "tail", "size cap set to 4.0 MiB", "commands",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("session output lacks %q", want)
		}
	}
	// The data is really on disk: reopen and look.
	db, err := engine.Open(p, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v, ok, _ := db.Get([]byte("b")); !ok || string(v) != "2" {
		t.Fatalf("b = %q %v", v, ok)
	}
	if _, ok, _ := db.Get([]byte("city")); ok {
		t.Fatal("city should have been deleted by the batch")
	}
	if db.Stats().MaxBytes != 4194304 {
		t.Fatalf("the cap was not persisted: %d", db.Stats().MaxBytes)
	}
}

func TestShellReportsFullWithoutChangingAnything(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db, err := engine.Open(p, engine.Options{MaxBytes: 12000, SetMaxBytes: true})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	out := session(t, p, "put k "+strings.Repeat("x", 9000)+"\ncount\nquit\n")
	if !strings.Contains(out, "ERR FULL") || !strings.Contains(out, "0 keys") {
		t.Fatalf("expected a refused put and no keys:\n%s", out)
	}
}

func TestSplitPathAcceptsFlagsBeforeOrAfter(t *testing.T) {
	for _, args := range [][]string{{"x.db", "--max-bytes", "5"}, {"--max-bytes", "5", "x.db"}} {
		path, rest := splitPath(args)
		if path != "x.db" || len(rest) != 2 {
			t.Fatalf("%v -> %q %v", args, path, rest)
		}
	}
}

func TestUnknownCommandAndHelp(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"bogus"}, &out, bufio.NewReader(strings.NewReader(""))); code != 2 {
		t.Fatal(code)
	}
	out.Reset()
	if code := run([]string{"help"}, &out, bufio.NewReader(strings.NewReader(""))); code != 0 || !strings.Contains(out.String(), "leapfrog demo") {
		t.Fatal(out.String())
	}
}

// The crash demo drives the real kvdb binary.
func TestCrashDemoAgainstTheRealServer(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	tui.SetColor(false)
	bin := filepath.Join(t.TempDir(), "kvdb")
	if b, err := exec.Command("go", "build", "-o", bin, "../kvdb").CombinedOutput(); err != nil {
		t.Fatalf("building kvdb: %v\n%s", err, b)
	}
	var out bytes.Buffer
	if !runDemo(&out, bin, 2) {
		t.Fatalf("the demo reported a violation:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "0 lost") || !strings.Contains(out.String(), "SIGKILL") {
		t.Fatalf("unexpected demo output:\n%s", out.String())
	}
}
