package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chethan-9817/pineapple/src/engine"
	"github.com/chethan-9817/pineapple/src/internal/envshell"
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

// The live viewer decodes the file on its own. It must agree with `kvdb dump` after puts, overwrites, deletes,
// an atomic batch and a compaction.
func TestLiveSnapshotAgreesWithKvdbDump(t *testing.T) {
	kvdb := repoKVDB(t)
	p := filepath.Join(t.TempDir(), "live.db")
	script := "put a 1\nput b 2\nput a \"one\"\nbegin\nput c 3\ndel b\nput d hex:00ff10\ncommit\nput big " + strings.Repeat("x", 3000) + "\n"
	cmd := exec.Command(kvdb, "serve", p)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	check := func(stage string) {
		t.Helper()
		snap := snapshotDB(p)
		if snap.Error != "" {
			t.Fatalf("%s: %s", stage, snap.Error)
		}
		dump, err := exec.Command(kvdb, "dump", p).Output()
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, l := range strings.Split(strings.TrimRight(string(dump), "\n"), "\n") {
			if l != "" {
				want = append(want, strings.SplitN(l, "\t", 2)[0])
			}
		}
		if len(snap.Items) != len(want) {
			t.Fatalf("%s: viewer has %d keys, kvdb dump has %d (%v)", stage, len(snap.Items), len(want), want)
		}
		for i, it := range snap.Items {
			if it.Key != want[i] {
				t.Fatalf("%s: key %d: viewer %q, dump %q", stage, i, it.Key, want[i])
			}
		}
	}
	check("after writes")
	snap := snapshotDB(p)
	got := map[string]string{}
	for _, it := range snap.Items {
		got[it.Key] = it.Value
	}
	if got["a"] != `"one"` || got["c"] != "3" || got["d"] != `hex:00ff10` {
		t.Fatalf("values wrong: a=%q c=%q d=%q", got["a"], got["c"], got["d"])
	}
	if _, ok := got["b"]; ok {
		t.Fatal("deleted key b is still shown")
	}
	c := exec.Command(kvdb, "serve", p)
	c.Stdin = strings.NewReader("compact\n")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	check("after compaction")
	if s := snapshotDB(filepath.Join(t.TempDir(), "missing.db")); s.Exists || s.Error == "" {
		t.Fatalf("a missing file must be reported, not crash: %+v", s)
	}
}

func TestLiveHandlerServesStateAndPage(t *testing.T) {
	kvdb := repoKVDB(t)
	p := filepath.Join(t.TempDir(), "h.db")
	c := exec.Command(kvdb, "put", p, "student:1", "name=Asha")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	envshell.SetActiveDB(p)
	t.Cleanup(envshell.ClearActive)
	var mu sync.Mutex
	srv := httptest.NewServer(liveHandler(p, &mu))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var s liveSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if s.Keys != 1 || s.Items[0].Key != "student:1" || s.Items[0].Value != "name=Asha" {
		t.Fatalf("%+v", s)
	}
	page, _ := http.Get(srv.URL + "/")
	b, _ := io.ReadAll(page.Body)
	if !strings.Contains(string(b), "LEAPFROG-R") {
		t.Fatal("page missing")
	}
}

// repoKVDB returns the repository's bin/kvdb (tests run inside the package directory, so look upwards).
func repoKVDB(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../../bin/kvdb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skip("bin/kvdb not built: run `make build`")
	}
	return p
}
