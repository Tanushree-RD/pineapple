package main

// Recovery fixtures (SPEC 15.8, `--mode fixtures`): crafted database files with an exact expected
// result. Each fixture starts from a REAL database made with the kvdb CLI, then cuts or corrupts it
// at offsets computed from SPEC 4 (by HYDRA's own reader, diskformat.go), and finally asserts what
// `kvdb open`, `kvdb check` and `kvdb dump` say.
//
// Every expectation is derived two independent ways before kvdb is consulted: explicitly by
// construction ("the last frame is gone, so keys a and b remain") and by decoding the modified
// bytes with HYDRA's reader. If those two disagree, the fixture or the reader is wrong and the run
// says so; only then is the real database judged against them.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func init() { modes["fixtures"] = runFixtures }

// fxEnv is the scratch area and the kvdb under test.
type fxEnv struct {
	kvdb string
	root string
}

// fxWant is the expected outcome for one database file.
type fxWant struct {
	keys      map[string]string // expected dump
	tail      string            // COMPLETE | TORN | SHORT_SENTINEL ("" = do not assert)
	check     string            // "OK" | "CORRUPT" | "ANY" (record what it says)
	infoHas   []string          // substrings the check output must contain
	maxBytes  string            // expected max_bytes in open output ("" = do not assert)
	openFails bool              // open must exit non-zero and leave the file untouched
	gone      []string          // files that must not exist after open (stale temps)
}

type fxResult struct {
	name     string
	problems []string
	note     string
}

type fixture struct {
	name string
	run  func(e *fxEnv, dir string) (*fxWant, string, error) // returns want and the db path to judge
}

func runFixtures(cfg *Config) int {
	if _, err := os.Stat(cfg.KVDB); err != nil {
		fmt.Fprintf(os.Stderr, "torture: kvdb binary not found at %s (run `make` first, or pass --kvdb)\n", cfg.KVDB)
		return 2
	}
	root := cfg.WorkDir
	if root == "" {
		d, err := os.MkdirTemp("", "hydra-fx-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "torture: %v\n", err)
			return 2
		}
		root = d
	} else if err := os.MkdirAll(root, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "torture: %v\n", err)
		return 2
	}
	e := &fxEnv{kvdb: cfg.KVDB, root: root}
	fails := 0
	var all []fixture
	all = append(all, fixtureList()...)
	for _, f := range all {
		dir := filepath.Join(root, f.name)
		_ = os.RemoveAll(dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "torture: %v\n", err)
			return 2
		}
		res := e.runOne(f, dir)
		if len(res.problems) == 0 {
			line := fmt.Sprintf("fixture %-34s PASS", f.name)
			if res.note != "" {
				line += "  (" + res.note + ")"
			}
			fmt.Println(line)
			continue
		}
		fails++
		fmt.Printf("fixture %-34s FAIL\n", f.name)
		for _, p := range res.problems {
			fmt.Println("  - " + p)
		}
	}
	if !cfg.Keep && cfg.WorkDir == "" && fails == 0 {
		_ = os.RemoveAll(root)
	} else {
		fmt.Printf("fixture files kept in %s\n", root)
	}
	if fails > 0 {
		fmt.Printf("RESULT FAIL %d of %d fixtures\n", fails, len(all))
		return 1
	}
	fmt.Printf("RESULT PASS %d/%d fixtures\n", len(all), len(all))
	return 0
}

func (e *fxEnv) runOne(f fixture, dir string) fxResult {
	res := fxResult{name: f.name}
	want, db, err := f.run(e, dir)
	if err != nil {
		res.problems = append(res.problems, "fixture construction failed: "+err.Error())
		return res
	}
	res.problems, res.note = e.judge(db, want)
	return res
}

// ---------------------------------------------------------------------------------------------
// Judging one database file against its expectation.

func (e *fxEnv) judge(db string, w *fxWant) (problems []string, note string) {
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	before, beforeSize, derr := fileDigest(db)
	if derr != nil {
		bad("cannot read fixture: %v", derr)
		return
	}

	// Independent derivation: decode the bytes ourselves.
	data, _ := os.ReadFile(db)
	var im *Image
	if !w.openFails {
		var err error
		im, err = ReadImage(data)
		if err != nil {
			bad("HYDRA's own reader cannot decode the fixture: %v", err)
			return
		}
		if w.tail != "" && im.Tail != w.tail {
			bad("the file decodes (HYDRA's reader) as tail=%s but %s was expected: either the fixture is wrong or kvdb wrote a bad file", im.Tail, w.tail)
		}
		if got := stateAsStrings(im.State); !equalMaps(got, w.keys) {
			bad("the file decodes (HYDRA's reader) to %v but %v was expected: either the fixture is wrong or kvdb wrote a bad file", got, w.keys)
		}
	}

	out, errOut, code, err := runKVDB(e.kvdb, time.Minute, "open", db)
	if err != nil {
		bad("kvdb open: %v", err)
		return
	}
	if w.openFails {
		if code == 0 {
			bad("kvdb open succeeded on a file with no valid superblock; it must fail and never re-initialise (SPEC 7.1 step 4)")
		}
		if after, afterSize, _ := fileDigest(db); after != before || afterSize != beforeSize {
			bad("kvdb open modified a file it could not open (size %d -> %d)", beforeSize, afterSize)
		}
		return problems, "open failed as required"
	}
	if code != 0 {
		bad("kvdb open exited %d: %s", code, truncate(strings.TrimSpace(errOut+out), 200))
		return
	}
	of := parseKV(strings.TrimSpace(out))
	if w.tail != "" && of["tail"] != w.tail {
		bad("kvdb open says tail=%s, expected %s", of["tail"], w.tail)
	}
	if of["keys"] != strconv.Itoa(len(w.keys)) {
		bad("kvdb open says keys=%s, expected %d", of["keys"], len(w.keys))
	}
	if w.maxBytes != "" && of["max_bytes"] != w.maxBytes {
		bad("kvdb open says max_bytes=%s, expected %s", of["max_bytes"], w.maxBytes)
	}

	cout, _, ccode, err := runKVDB(e.kvdb, time.Minute, "check", db)
	if err != nil {
		bad("kvdb check: %v", err)
	} else {
		first := strings.SplitN(strings.TrimSpace(cout), "\n", 2)[0]
		switch w.check {
		case "OK":
			if ccode != 0 || first != "OK" {
				bad("kvdb check printed %q (exit %d), expected OK", truncate(first, 120), ccode)
			}
		case "CORRUPT":
			if ccode == 0 || !strings.HasPrefix(first, "CORRUPT") {
				bad("kvdb check printed %q (exit %d), expected CORRUPT", truncate(first, 120), ccode)
			}
		default:
			note = "check: " + truncate(first, 70)
		}
		for _, s := range w.infoHas {
			if !strings.Contains(cout, s) {
				bad("kvdb check output lacks %q:\n%s", s, indent(truncate(strings.TrimSpace(cout), 600)))
			}
		}
		if im != nil {
			if want := "frames=" + strconv.Itoa(len(im.Frames)); w.check == "OK" && !strings.Contains(cout, want) {
				bad("kvdb check does not report %s although HYDRA's reader counts %d frames", want, len(im.Frames))
			}
		}
	}

	dout, derrOut, dcode, err := runKVDB(e.kvdb, time.Minute, "dump", db)
	if err != nil || dcode != 0 {
		bad("kvdb dump failed (exit %d): %v %s", dcode, err, truncate(strings.TrimSpace(derrOut), 200))
	} else if parsed, perr := ParseDump(strings.NewReader(dout)); perr != nil {
		bad("kvdb dump output is malformed: %v", perr)
	} else if got := stateAsStrings(parsed); !equalMaps(got, w.keys) {
		bad("kvdb dump shows %v, expected %v", got, w.keys)
	}

	for _, g := range w.gone {
		if _, err := os.Stat(g); err == nil {
			bad("stale temp file %s still exists after open (SPEC 6.2)", filepath.Base(g))
		}
	}
	if after, afterSize, _ := fileDigest(db); after != before || afterSize != beforeSize {
		bad("open/check/dump modified the file (size %d -> %d): recovery must write no bytes", beforeSize, afterSize)
	}
	return
}

func indent(s string) string { return "      " + strings.ReplaceAll(s, "\n", "\n      ") }

func stateAsStrings(m map[string][]byte) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = string(v)
	}
	return out
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------------------------
// Building fixtures from a real database.

func (e *fxEnv) kv(args ...string) error {
	out, errOut, code, err := runKVDB(e.kvdb, time.Minute, args...)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("kvdb %s exited %d: %s", strings.Join(args, " "), code, truncate(strings.TrimSpace(out+errOut), 200))
	}
	return nil
}

// serveText runs `kvdb serve <db>` in text mode, feeds it the lines and returns its stdout.
func (e *fxEnv) serveText(db string, lines ...string) (string, error) {
	cmd := exec.Command(e.kvdb, "serve", db)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return out.String(), fmt.Errorf("kvdb serve: %v: %s", err, truncate(errb.String(), 200))
		}
	case <-time.After(time.Minute):
		_ = cmd.Process.Kill()
		return out.String(), fmt.Errorf("kvdb serve timed out")
	}
	return out.String(), nil
}

// threePuts makes a database holding a=1, b=2, c=3, one frame each.
func (e *fxEnv) threePuts(db string) error {
	for _, kv := range [][2]string{{"a", "1"}, {"b", "2"}, {"c", "3"}} {
		if err := e.kv("put", db, kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

func readImage(db string) (*Image, []byte, error) {
	data, err := os.ReadFile(db)
	if err != nil {
		return nil, nil, err
	}
	im, err := ReadImage(data)
	return im, data, err
}

func writeVariant(dir, name string, data []byte) (string, error) {
	p := filepath.Join(dir, name)
	return p, os.WriteFile(p, data, 0o644)
}

func keysOf(pairs ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m
}

func sortedNames(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
