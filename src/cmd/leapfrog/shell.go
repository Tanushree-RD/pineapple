package main

// The interactive shell: `leapfrog shell <db>`. It opens the database in this process through the
// engine package, so every put really is an fsync, and shows how long it took.

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chethan-9817/pineapple/src/engine"
	"github.com/chethan-9817/pineapple/src/internal/tui"
)

type shell struct {
	path    string
	db      *engine.DB
	out     io.Writer
	in      *bufio.Reader
	kvdb    string
	opened  engine.OpenStats
	batch   []engine.Op
	inBatch bool
	puts    int
	started time.Time
}

func openShell(path string, capBytes int64, setCap bool, out io.Writer, in *bufio.Reader, kvdb string) (*shell, error) {
	s := &shell{path: path, out: out, in: in, kvdb: kvdb, started: time.Now()}
	if err := s.open(capBytes, setCap); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *shell) open(capBytes int64, setCap bool) error {
	db, err := engine.Open(s.path, engine.Options{MaxBytes: capBytes, SetMaxBytes: setCap})
	if err != nil {
		return err
	}
	s.db, s.opened = db, db.Stats()
	return nil
}

// decodeArg turns a typed key or value into bytes: a "hex:" prefix is hex, everything else is text.
func decodeArg(s string) ([]byte, error) {
	if strings.HasPrefix(s, "hex:") {
		return hex.DecodeString(s[4:])
	}
	return []byte(s), nil
}

func (s *shell) prompt() string {
	name := filepath.Base(s.path)
	mode := ""
	if s.inBatch {
		mode = tui.Yellow(fmt.Sprintf(" [batch: %d queued]", len(s.batch)))
	}
	return fmt.Sprintf("%s %s%s %s ", tui.Bold(tui.Gradient("leapfrog", 39, 45)), tui.Gray(name), mode, tui.Cyan("❯"))
}

func (s *shell) p(format string, a ...any) { fmt.Fprintf(s.out, format+"\n", a...) }

func (s *shell) errMsg(err error) {
	switch {
	case errors.Is(err, engine.ErrFull):
		s.p("%s %s", tui.Fail(), tui.Red("ERR FULL")+tui.Gray(" – refused before any byte was written; the database is unchanged"))
	case errors.Is(err, engine.ErrIO):
		s.p("%s %s", tui.Fail(), tui.Red("ERR IO")+tui.Gray(" – a write or fsync failed; the handle is poisoned and refuses writes (reopen to recover)"))
	case errors.Is(err, engine.ErrInvalid):
		s.p("%s %s", tui.Fail(), tui.Red("ERR INVALID")+tui.Gray(" – bad key or value (key 1..1024 bytes, value up to 1 MiB)"))
	default:
		s.p("%s %s", tui.Fail(), tui.Red("ERR "+err.Error()))
	}
}

func timeIt(f func() error) (time.Duration, error) {
	t0 := time.Now()
	err := f()
	return time.Since(t0), err
}

func ms(d time.Duration) string { return fmt.Sprintf("%.2f ms", float64(d.Microseconds())/1000) }

// run is the read-eval-print loop.
func (s *shell) run() {
	for {
		fmt.Fprint(s.out, s.prompt())
		line, err := s.in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			if quit := s.exec(line); quit {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func splitOnce(s string) (string, string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

func (s *shell) exec(line string) (quit bool) {
	cmd, rest := splitOnce(line)
	switch strings.ToLower(cmd) {
	case "put", "set":
		k, v := splitOnce(rest)
		s.put(k, v, strings.Contains(rest, " ") || v != "")
	case "get":
		s.get(rest)
	case "del", "delete", "rm":
		s.del(rest)
	case "begin":
		s.inBatch, s.batch = true, nil
		s.p("%s batch started: put/del are queued and commit as ONE atomic frame", tui.Cyan("▸"))
	case "commit":
		s.commit()
	case "abort":
		s.inBatch, s.batch = false, nil
		s.p("%s batch discarded", tui.Cyan("▸"))
	case "dump", "ls", "list":
		s.dump(rest)
	case "count":
		n := 0
		_ = s.db.ForEach(func(k, v []byte) error { n++; return nil })
		s.p("%d keys", n)
	case "stats", "info":
		s.stats()
	case "check":
		s.check()
	case "compact":
		s.compact()
	case "cap":
		s.setCap(rest)
	case "inspect", "bytes":
		s.inspect(strings.Contains(rest, "-v") || strings.Contains(rest, "anatomy"))
	case "fill":
		s.fill(rest)
	case "crash", "demo":
		s.crash()
	case "help", "?":
		s.help()
	case "clear", "cls":
		fmt.Fprint(s.out, "\x1b[2J\x1b[H")
	case "quit", "exit", "q":
		return true
	default:
		s.p("%s unknown command %q. Type %s for the list.", tui.Warn(), cmd, tui.Bold("help"))
	}
	return false
}

func (s *shell) put(ks, vs string, hasValue bool) {
	if ks == "" {
		s.p("usage: put <key> <value>")
		return
	}
	if !hasValue {
		s.p("usage: put <key> <value>   (use hex:00ff for binary; an empty value is hex:)")
		return
	}
	k, e1 := decodeArg(ks)
	v, e2 := decodeArg(vs)
	if e1 != nil || e2 != nil {
		s.p("%s bad hex: argument", tui.Fail())
		return
	}
	if s.inBatch {
		s.batch = append(s.batch, engine.Op{Kind: engine.OpPut, Key: k, Value: v})
		s.p("%s queued put %s (%s in batch)", tui.Cyan("▸"), tui.Bold(esc(k)), plural(len(s.batch), "op"))
		return
	}
	d, err := timeIt(func() error { return s.db.Put(k, v) })
	if err != nil {
		s.errMsg(err)
		return
	}
	s.puts++
	s.p("%s %s %s", tui.OK(), tui.Green("stored"), tui.Gray(fmt.Sprintf("%s = %s · durable on disk in %s (write + fsync)", esc(k), tui.Trunc(esc(v), 40), ms(d))))
}

func (s *shell) get(ks string) {
	if ks == "" {
		s.p("usage: get <key>")
		return
	}
	k, err := decodeArg(ks)
	if err != nil {
		s.p("%s bad hex: argument", tui.Fail())
		return
	}
	v, found, err := s.db.Get(k)
	switch {
	case err != nil:
		s.errMsg(err)
	case !found:
		s.p("%s %s", tui.Warn(), tui.Yellow("NOTFOUND"))
	default:
		s.p("%s %s %s", tui.OK(), esc(v), tui.Gray(fmt.Sprintf("(%s)", tui.Bytes(int64(len(v))))))
	}
}

func (s *shell) del(ks string) {
	if ks == "" {
		s.p("usage: del <key>")
		return
	}
	k, err := decodeArg(ks)
	if err != nil {
		s.p("%s bad hex: argument", tui.Fail())
		return
	}
	if s.inBatch {
		s.batch = append(s.batch, engine.Op{Kind: engine.OpDelete, Key: k})
		s.p("%s queued del %s (%s in batch)", tui.Cyan("▸"), tui.Bold(esc(k)), plural(len(s.batch), "op"))
		return
	}
	d, err := timeIt(func() error { return s.db.Delete(k) })
	if err != nil {
		s.errMsg(err)
		return
	}
	s.p("%s %s %s", tui.OK(), tui.Green("deleted"), tui.Gray(fmt.Sprintf("%s · durable in %s", esc(k), ms(d))))
}

func (s *shell) commit() {
	if !s.inBatch {
		s.p("no batch open: type begin first")
		return
	}
	ops := s.batch
	s.inBatch, s.batch = false, nil
	if len(ops) == 0 {
		s.p("empty batch, nothing to do")
		return
	}
	d, err := timeIt(func() error { return s.db.Batch(ops) })
	if err != nil {
		s.errMsg(err)
		return
	}
	s.p("%s %s %s", tui.OK(), tui.Green(fmt.Sprintf("committed %d ops atomically", len(ops))), tui.Gray(fmt.Sprintf("· one frame, one fsync, %s", ms(d))))
}

func (s *shell) dump(arg string) {
	prefix, limit := arg, 25
	if f := strings.Fields(arg); len(f) == 2 {
		if n, err := strconv.Atoi(f[1]); err == nil {
			prefix, limit = f[0], n
		}
	}
	type row struct{ k, v string }
	var rows []row
	total := 0
	_ = s.db.ForEach(func(k, v []byte) error {
		total++
		if prefix == "" || bytes.HasPrefix(k, []byte(prefix)) {
			rows = append(rows, row{esc(k), esc(v)})
		}
		return nil
	})
	if len(rows) == 0 {
		s.p("%s", tui.Gray("(no keys)"))
		return
	}
	w := tui.Width()
	kw := 22
	var lines []string
	lines = append(lines, tui.Bold(tui.PadRight("KEY", kw))+"  "+tui.Bold("VALUE"))
	for i, r := range rows {
		if i == limit {
			lines = append(lines, tui.Gray(fmt.Sprintf("… %d more (dump <prefix> <limit>)", len(rows)-limit)))
			break
		}
		lines = append(lines, tui.Cyan(tui.PadRight(tui.Trunc(r.k, kw), kw))+"  "+tui.Trunc(r.v, w-kw-8))
	}
	for _, l := range tui.Box(fmt.Sprintf("%d of %d keys, ascending byte order", len(rows), total), lines, w, tui.Blue) {
		fmt.Fprintln(s.out, l)
	}
}

func (s *shell) stats() {
	st, _ := os.Stat(s.path)
	size := int64(0)
	if st != nil {
		size = st.Size()
	}
	n := 0
	_ = s.db.ForEach(func(k, v []byte) error { n++; return nil })
	capS := "none"
	if s.opened.MaxBytes != 0 {
		capS = tui.Bytes(s.opened.MaxBytes) + fmt.Sprintf(" (%.0f%% used)", 100*float64(size)/float64(s.opened.MaxBytes))
	}
	lines := []string{
		fmt.Sprintf("%s  %s", tui.Gray("file       "), s.path),
		fmt.Sprintf("%s  %s", tui.Gray("size       "), tui.Bytes(size)),
		fmt.Sprintf("%s  %d", tui.Gray("keys       "), n),
		fmt.Sprintf("%s  %s", tui.Gray("size cap   "), capS),
		fmt.Sprintf("%s  %s, tail was %s, recovery took %s", tui.Gray("opened     "), "recovered read-only", tui.Bold(s.opened.Tail), s.opened.Recovery.Round(time.Microsecond)),
		fmt.Sprintf("%s  %d durable puts so far, shell up %s", tui.Gray("session    "), s.puts, tui.Dur(time.Since(s.started))),
	}
	for _, l := range tui.Box("stats", lines, tui.Width(), tui.Magenta) {
		fmt.Fprintln(s.out, l)
	}
}

func (s *shell) reopen() {
	capB, set := s.opened.MaxBytes, false
	if err := s.open(capB, set); err != nil {
		s.p("%s could not reopen: %v", tui.Fail(), err)
		os.Exit(1)
	}
}

func (s *shell) check() {
	_ = s.db.Close()
	t0 := time.Now()
	rep, err := engine.Check(s.path)
	d := time.Since(t0)
	s.reopen()
	if err != nil {
		s.p("%s %v", tui.Fail(), err)
		return
	}
	head := tui.OK() + " " + tui.Green(tui.Bold("OK"))
	if !rep.OK {
		head = tui.Fail() + " " + tui.Red(tui.Bold("CORRUPT")) + " " + rep.Reason
	}
	lines := []string{head + tui.Gray(fmt.Sprintf("   (full scan took %s)", d.Round(time.Microsecond)))}
	for _, in := range rep.Info {
		lines = append(lines, tui.Gray("• ")+in)
	}
	for _, l := range tui.Box("check", lines, tui.Width(), tui.Green) {
		fmt.Fprintln(s.out, l)
	}
}

func (s *shell) compact() {
	before := fileSize(s.path)
	d, err := timeIt(s.db.Compact)
	if err != nil {
		s.errMsg(err)
		return
	}
	after := fileSize(s.path)
	note := "live keys rewritten, temp file renamed over the database atomically"
	if after >= before {
		note = "nothing to reclaim here, and the new file carries a checkpoint index, so it is not smaller"
	}
	s.p("%s %s %s → %s %s", tui.OK(), tui.Green("compacted"), tui.Bytes(before), tui.Bold(tui.Bytes(after)), tui.Gray(fmt.Sprintf("in %s: %s", ms(d), note)))
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func (s *shell) setCap(arg string) {
	arg = strings.TrimSpace(arg)
	var n int64
	switch strings.ToLower(arg) {
	case "":
		s.p("usage: cap <bytes>   (or: cap off)   e.g. cap 4194304")
		return
	case "off", "none", "0":
		n = 0
	default:
		v, err := strconv.ParseInt(arg, 10, 64)
		if err != nil || v < 0 {
			s.p("%s not a number: %q", tui.Fail(), arg)
			return
		}
		n = v
	}
	_ = s.db.Close()
	if err := s.open(n, true); err != nil {
		s.p("%s %v", tui.Fail(), err)
		s.reopen()
		return
	}
	if n == 0 {
		s.p("%s size cap removed (persisted in the superblock)", tui.OK())
	} else {
		s.p("%s size cap set to %s and persisted in the superblock: every later process enforces it", tui.OK(), tui.Bold(tui.Bytes(n)))
	}
}

func (s *shell) inspect(verbose bool) {
	im, err := readImage(s.path)
	if err != nil {
		s.p("%s %v", tui.Fail(), err)
		return
	}
	printImage(s.out, s.path, im, verbose)
}

// fill writes n keys with a progress bar, to get a database worth looking at.
func (s *shell) fill(arg string) {
	f := strings.Fields(arg)
	n, size := 1000, 100
	if len(f) > 0 {
		if v, err := strconv.Atoi(f[0]); err == nil && v > 0 {
			n = v
		}
	}
	if len(f) > 1 {
		if v, err := strconv.Atoi(f[1]); err == nil && v >= 0 {
			size = v
		}
	}
	val := bytes.Repeat([]byte("x"), size)
	t0 := time.Now()
	height := 0
	const perBatch = 50
	done := 0
	for done < n {
		cnt := perBatch
		if n-done < cnt {
			cnt = n - done
		}
		ops := make([]engine.Op, cnt)
		for i := range ops {
			ops[i] = engine.Op{Kind: engine.OpPut, Key: []byte(fmt.Sprintf("fill-%07d", s.puts+done+i)), Value: val}
		}
		if err := s.db.Batch(ops); err != nil {
			if height > 0 {
				fmt.Fprintln(s.out)
			}
			s.errMsg(err)
			s.p("%s", tui.Gray(fmt.Sprintf("stopped after %d of %d keys", done, n)))
			s.puts += done
			return
		}
		done += cnt
		rate := float64(done) / time.Since(t0).Seconds()
		if !tui.IsTTY() {
			continue // no cursor control without a terminal: only print the summary
		}
		height = tui.Redraw(s.out, height, []string{fmt.Sprintf("  %s %d/%d  %s", tui.Bar(float64(done)/float64(n), 30), done, n, tui.Gray(fmt.Sprintf("%.0f keys/s, each batch of %d is one fsync", rate, perBatch)))})
	}
	s.puts += n
	s.p("%s wrote %d keys of %s in %s", tui.OK(), n, tui.Bytes(int64(size)), time.Since(t0).Round(time.Millisecond))
}

func (s *shell) crash() {
	if s.kvdb == "" {
		s.p("%s the crash demo needs the kvdb binary (run `make`, or start with --kvdb PATH)", tui.Warn())
		return
	}
	_ = s.db.Close()
	runDemo(s.out, s.kvdb, 1)
	s.reopen()
}

func (s *shell) help() {
	rows := [][2]string{
		{"put <key> <value>", "store a key; shows the real fsync time (hex:00ff for binary)"},
		{"get <key>  ·  del <key>", "read or delete a key"},
		{"begin … commit | abort", "queue puts and dels, apply them as ONE atomic frame"},
		{"dump [prefix] [limit]", "list keys in ascending byte order"},
		{"count  ·  stats", "key count, file size, cap, recovery time"},
		{"check", "full integrity scan (SPEC 13)"},
		{"compact", "rewrite only live keys; the file shrinks"},
		{"cap <bytes|off>", "persist a size cap in the superblock"},
		{"fill <n> [bytes]", "write n keys in batches, with a progress bar"},
		{"inspect [-v]", "decode the file: superblocks, frames, tail; -v labels every byte of a frame"},
		{"crash", "kill -9 a live server and prove nothing acknowledged was lost"},
		{"clear · help · quit", ""},
	}
	var names []string
	for _, r := range rows {
		names = append(names, r[0])
	}
	w := 0
	for _, n := range names {
		if len(n) > w {
			w = len(n)
		}
	}
	var lines []string
	for _, r := range rows {
		lines = append(lines, tui.Cyan(tui.PadRight(r[0], w))+"  "+tui.Gray(r[1]))
	}
	for _, l := range tui.Box("commands", lines, tui.Width(), tui.Blue) {
		fmt.Fprintln(s.out, l)
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
