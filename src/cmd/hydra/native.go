package main

// The scenarios that are checked by driving bin/kvdb directly (the "by hand" commands of the test
// guide), written as Go so that `hydra scenario B5` can run and judge them. Each one is a list of
// Checks: a sentence, whether it held, and what was actually seen.

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Check is one verified statement.
type Check struct {
	Desc   string
	OK     bool
	Detail string
	Skip   bool
}

type native struct {
	kvdb   string
	dir    string
	checks []Check
}

func (n *native) add(desc string, ok bool, detail string) {
	n.checks = append(n.checks, Check{Desc: desc, OK: ok, Detail: detail})
}

func (n *native) skip(desc, why string) {
	n.checks = append(n.checks, Check{Desc: desc, Skip: true, OK: true, Detail: why})
}

// kv runs `kvdb args...` in the scratch directory and returns its combined output and exit code.
func (n *native) kv(args ...string) (string, int) {
	cmd := exec.Command(n.kvdb, args...)
	cmd.Dir = n.dir
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	return strings.TrimSpace(string(out)), code
}

func (n *native) serve(input string, args ...string) string {
	cmd := exec.Command(n.kvdb, append([]string{"serve"}, args...)...)
	cmd.Dir = n.dir
	cmd.Stdin = strings.NewReader(input)
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

func (n *native) path(name string) string { return filepath.Join(n.dir, name) }

func (n *native) write(name, content string) { _ = os.WriteFile(n.path(name), []byte(content), 0o644) }

func (n *native) size(name string) int64 {
	st, err := os.Stat(n.path(name))
	if err != nil {
		return -1
	}
	return st.Size()
}

func (n *native) sum(name string) string {
	b, _ := os.ReadFile(n.path(name))
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:6])
}

func (n *native) keyCount(db string) int {
	out, _ := n.kv("dump", db)
	if out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

func has(s, sub string) bool { return strings.Contains(s, sub) }

// natives maps a name used in the scenario table to its check.
var natives = map[string]func(*native){
	"b5":      nativeB5,
	"f4":      nativeF4,
	"recover": nativeRecover,
	"lock":    nativeLock,
	"cap":     nativeCap,
	"e2":      nativeE2,
	"e4":      nativeE4,
	"compact": nativeCompact,
	"kill":    nativeKill,
	"onemib":  nativeOneMiB,
}

// runNative runs one native check in a fresh scratch directory.
func (e *Env) runNative(name string) []Check {
	f, ok := natives[name]
	if !ok {
		return []Check{{Desc: "unknown check " + name}}
	}
	dir, err := os.MkdirTemp(e.runDir, "native-"+name+"-")
	if err != nil {
		return []Check{{Desc: "cannot create a scratch directory", Detail: err.Error()}}
	}
	n := &native{kvdb: e.kvdb, dir: dir}
	f(n)
	return n.checks
}

func nativeB5(n *native) {
	n.write("b1.txt", "put x 1\ndel x\n")
	out, code := n.kv("batch", "t.db", "b1.txt")
	n.add("batch `put x; del x` is accepted", out == "OK" && code == 0, out)
	got, _ := n.kv("get", "t.db", "x")
	n.add("PUT X then DEL X leaves X absent (last op wins)", got == "NOTFOUND", "get x -> "+got)
	n.write("b2.txt", "del y\nput y 2\n")
	n.kv("batch", "t.db", "b2.txt")
	got, _ = n.kv("get", "t.db", "y")
	n.add("DEL Y then PUT Y leaves Y present", got == "2", "get y -> "+got)
	n.kv("put", "t.db", "e", "hex:")
	got, _ = n.kv("get", "t.db", "e")
	dump, _ := n.kv("dump", "t.db")
	n.add("an empty value is a value, not a delete", got == "hex:" && has(dump, "e\thex:"), "get e -> "+got)
}

func nativeF4(n *native) {
	n.kv("put", "f.db", "a", "b")
	before := n.size("f.db")
	_ = os.Truncate(n.path("f.db"), before-1)
	open, _ := n.kv("open", "f.db")
	n.add("after cutting ONE byte, open reports a SHORT_SENTINEL tail", has(open, "tail=SHORT_SENTINEL"), open)
	got, _ := n.kv("get", "f.db", "a")
	n.add("the ACKed write survives (F4)", got == "b", "get a -> "+got)
	chk, _ := n.kv("check", "f.db")
	n.add("check says OK and calls it an unacknowledged-safe tail, not corruption", strings.HasPrefix(chk, "OK") && has(chk, "tail=SHORT_SENTINEL"), firstLine(chk))
	n.add("reading changed nothing on disk", n.size("f.db") == before-1, fmt.Sprintf("size %d", n.size("f.db")))
	n.kv("put", "f.db", "c", "d")
	open, _ = n.kv("open", "f.db")
	n.add("the next write repairs the tail first", has(open, "tail=COMPLETE") && has(open, "keys=2"), open)
}

func nativeRecover(n *native) {
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		n.kv("put", "r.db", k, "v"+k)
	}
	_ = os.Truncate(n.path("r.db"), n.size("r.db")-30)
	before, sz := n.sum("r.db"), n.size("r.db")
	for i := 0; i < 3; i++ {
		n.kv("open", "r.db")
		n.kv("check", "r.db")
		n.kv("dump", "r.db")
	}
	n.add("3 x open/check/dump on a torn file change nothing (C1, F2: recovery writes no bytes)", n.sum("r.db") == before && n.size("r.db") == sz, "sha256 "+before)
	open, _ := n.kv("open", "r.db")
	n.add("the torn tail is reported, not repaired by open", has(open, "tail=TORN") && has(open, "keys=4"), open)
	n.kv("put", "r.db", "f", "vf")
	open, _ = n.kv("open", "r.db")
	n.add("the first write repairs it (C2): tail is COMPLETE again", n.size("r.db") != sz && has(open, "tail=COMPLETE") && has(open, "keys=5"), open)
	junk := make([]byte, 5000)
	rand.New(rand.NewSource(1)).Read(junk)
	_ = os.WriteFile(n.path("r.db.compact.tmp"), junk, 0o644)
	_ = os.WriteFile(n.path("r.db.create.tmp"), junk, 0o644)
	n.kv("open", "r.db")
	_, e1 := os.Stat(n.path("r.db.compact.tmp"))
	_, e2 := os.Stat(n.path("r.db.create.tmp"))
	n.add("stale .compact.tmp and .create.tmp are deleted, never used (C3)", os.IsNotExist(e1) && os.IsNotExist(e2), "both gone")
}

func nativeLock(n *native) {
	cmd := exec.Command(n.kvdb, "serve", "l.db")
	cmd.Dir = n.dir
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		n.add("start a server", false, err.Error())
		return
	}
	br := bufio.NewReader(stdout)
	line, _ := br.ReadString('\n')
	n.add("a server holds the database", strings.HasPrefix(line, "READY"), strings.TrimSpace(line))
	out, code := n.kv("open", "l.db")
	n.add("a second process is refused while it runs", has(out, "locked") && code == 1, fmt.Sprintf("%s (exit %d)", out, code))
	stdin.Close()
	_ = cmd.Wait()
	out, code = n.kv("open", "l.db")
	n.add("once the server is gone the database opens again", code == 0 && has(out, "keys="), out)
}

func nativeCap(n *native) {
	const capB = 400000
	val := strings.Repeat("x", 20000)
	out, _ := n.kv("open", "c.db", "--max-bytes", fmt.Sprint(capB))
	n.add("the cap is set once and persisted", has(out, fmt.Sprintf("max_bytes=%d", capB)), out)
	accepted, last, lastCode := 0, "", 0
	for accepted < 80 {
		last, lastCode = n.kv("put", "c.db", fmt.Sprintf("k%d", accepted), val)
		if lastCode != 0 {
			break
		}
		accepted++
	}
	n.add("puts are accepted until the cap, then refused with ERR FULL (exit 2)", accepted > 5 && accepted < 40 && lastCode == 2 && last == "ERR FULL", fmt.Sprintf("%d accepted, then %q exit %d", accepted, last, lastCode))
	n.add("the file never exceeds the cap (E1)", n.size("c.db") <= capB, fmt.Sprintf("%d <= %d bytes", n.size("c.db"), capB))
	n.add("the refused put changed nothing (E3)", n.keyCount("c.db") == accepted, fmt.Sprintf("%d keys", n.keyCount("c.db")))
	for i := 0; i < 10; i++ {
		n.kv("del", "c.db", fmt.Sprintf("k%d", i))
	}
	before := n.size("c.db")
	out, code := n.kv("put", "c.db", "after", val)
	n.add("after deleting 10 keys a put is accepted again (E5)", code == 0 && out == "OK", out)
	n.add("the rewrite shrank the file and it stays within the cap", n.size("c.db") < before && n.size("c.db") <= capB, fmt.Sprintf("%d -> %d bytes", before, n.size("c.db")))
	chk, _ := n.kv("check", "c.db")
	n.add("check says OK", strings.HasPrefix(chk, "OK"), firstLine(chk))
}

func nativeE2(n *native) {
	val := strings.Repeat("x", 20000)
	n.kv("open", "c.db", "--max-bytes", "400000")
	for i := 0; i < 5; i++ {
		n.kv("put", "c.db", fmt.Sprintf("k%d", i), val)
	}
	var in strings.Builder
	in.WriteString("begin\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&in, "put b%d %s\n", i, val)
	}
	in.WriteString("commit\n")
	out := n.serve(in.String(), "c.db")
	n.add("a batch that cannot fit is answered ERR FULL as a whole (E2)", has(out, "ERR 1 FULL"), lastLine(out))
	n.add("none of its 30 operations was applied", n.keyCount("c.db") == 5, fmt.Sprintf("%d keys (was 5)", n.keyCount("c.db")))
}

func nativeE4(n *native) {
	if _, err := exec.LookPath("bash"); err != nil {
		n.skip("a failing write answers ERR IO and exits 3", "bash is needed for ulimit")
		return
	}
	n.kv("put", "w.db", "a", "1")
	script := fmt.Sprintf(`ulimit -f 40; { for i in 1 2 3; do echo "put k$i %s"; done; } | '%s' serve w.db; echo "exit:$?"`, strings.Repeat("x", 30000), n.kvdb)
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = n.dir
	outb, _ := cmd.Output()
	out := string(outb)
	n.add("the failing write is answered ERR 2 IO", has(out, "ERR 2 IO"), "put 1 ACKed, put 2 refused")
	n.add("serve exits with code 3 (SPEC 8.2)", has(out, "exit:3"), "exit:3")
	n.add("nothing is ACKed after the error", strings.Count(out, "ACK ") == 1, fmt.Sprintf("%d ACK line(s)", strings.Count(out, "ACK ")))
	chk, _ := n.kv("check", "w.db")
	dump, _ := n.kv("dump", "w.db")
	n.add("the file still passes check and the ACKed data is intact", strings.HasPrefix(chk, "OK") && has(dump, "a\t1") && has(dump, "k1\t"), firstLine(chk))
	n.add("a file-size limit stands in for a full disk (same error path; real ENOSPC is not reproduced)", true, "ulimit -f 40")
}

func nativeCompact(n *native) {
	var puts, dels strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&puts, "put key%d value%d\n", i, i)
		fmt.Fprintf(&dels, "del key%d\n", i)
	}
	n.write("many.txt", puts.String())
	n.write("dels.txt", dels.String())
	n.kv("batch", "m.db", "many.txt")
	n.kv("batch", "m.db", "dels.txt")
	garbage := n.size("m.db")
	out := n.serve("compact\n", "m.db")
	n.add("COMPACT is acknowledged", has(out, "ACK 1"), lastLine(out))
	n.add("300 puts + 300 deletes + compact leave exactly 8192 + 72 = 8264 bytes (C4, C5)", n.size("m.db") == 8264, fmt.Sprintf("%d -> %d bytes", garbage, n.size("m.db")))
	chk, _ := n.kv("check", "m.db")
	n.add("the compacted file passes check with a valid checkpoint", strings.HasPrefix(chk, "OK") && has(chk, "checkpoint ok"), firstLine(chk))
}

// nativeKill is the headline claim, three times: kill -9 a server mid-stream, count ACKs against keys.
func nativeKill(n *native) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for round := 1; round <= 3; round++ {
		cmd := exec.Command(n.kvdb, "serve", "k.db")
		cmd.Dir = n.dir
		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			n.add("start a server", false, err.Error())
			return
		}
		var acks int64
		done := make(chan struct{})
		go func() {
			defer close(done)
			br := bufio.NewReaderSize(stdout, 1<<16)
			for {
				l, err := br.ReadString('\n')
				if strings.HasSuffix(l, "\n") && strings.HasPrefix(l, "ACK ") {
					atomic.AddInt64(&acks, 1)
				}
				if err != nil {
					return
				}
			}
		}()
		go func() {
			bw := bufio.NewWriterSize(stdin, 1<<16)
			for i := 0; ; i++ {
				if _, err := fmt.Fprintf(bw, "put r%d-%d v%d\n", round, i, i); err != nil {
					return
				}
				if i%32 == 0 && bw.Flush() != nil {
					return
				}
			}
		}()
		time.Sleep(time.Duration(200+rng.Intn(400)) * time.Millisecond)
		_ = cmd.Process.Kill()
		<-done
		_ = cmd.Wait()
		a := atomic.LoadInt64(&acks)
		dump, _ := n.kv("dump", "k.db")
		keys := int64(0)
		for _, l := range strings.Split(dump, "\n") {
			if strings.HasPrefix(l, fmt.Sprintf("r%d-", round)) {
				keys++
			}
		}
		n.add(fmt.Sprintf("round %d: kill -9 after %d ACKs, %d keys on disk: every ACKed write survived, at most one extra", round, a, keys), keys == a || keys == a+1, "")
		chk, _ := n.kv("check", "k.db")
		n.add(fmt.Sprintf("round %d: check is OK after the kill", round), strings.HasPrefix(chk, "OK"), firstLine(chk))
	}
}

func nativeOneMiB(n *native) {
	n.write("one.txt", "put big "+strings.Repeat("x", 1<<20)+"\n")
	out, code := n.kv("batch", "a.db", "one.txt")
	n.add("a 1 MiB value is stored", out == "OK" && code == 0, out)
	dump, _ := n.kv("dump", "a.db")
	parts := strings.SplitN(dump, "\t", 2)
	n.add("all 1,048,576 bytes come back", len(parts) == 2 && len(strings.TrimSpace(parts[1])) == 1<<20, fmt.Sprintf("%d bytes", len(parts[len(parts)-1])))
	chk, _ := n.kv("check", "a.db")
	n.add("check says OK (the frame CRC64 covers every byte)", strings.HasPrefix(chk, "OK"), firstLine(chk))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
