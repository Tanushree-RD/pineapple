package main

// `leapfrog demo`: the claim of the whole project, live. A real `kvdb serve` process is fed writes as
// fast as it will take them, killed with SIGKILL at a random moment, and the database file is then
// opened and counted. Every write the server acknowledged must be on disk, plus at most ONE more:
// the transaction that was already durable when the process died but whose ACK had not been sent.

import (
	"bufio"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chethan-9817/pineapple/src/engine"
	"github.com/chethan-9817/pineapple/src/internal/tui"
)

type roundResult struct {
	round     int
	after     time.Duration
	acks      int64
	onDisk    int
	tail      string
	recovery  time.Duration
	verdict   string
	ok        bool
	keysTotal int
}

func runDemo(w io.Writer, kvdb string, rounds int) bool {
	dir, err := os.MkdirTemp(cacheBase(), "leapfrog-demo-")
	if err != nil {
		fmt.Fprintf(w, "%s %v\n", tui.Fail(), err)
		return false
	}
	defer os.RemoveAll(dir) // the directory this function just created, nothing else
	path := filepath.Join(dir, "demo.db")

	width := tui.Width()
	intro := []string{
		"A real " + tui.Bold("kvdb serve") + " process is fed writes as fast as it will take them,",
		"then killed with " + tui.Red(tui.Bold("SIGKILL")) + " at a random moment. Nothing gets to clean up.",
		"We then open the file and count. The promise: " + tui.Green("every ACKed write is on disk") + ",",
		"plus at most " + tui.Bold("one") + " more: the transaction in flight when the process died.",
	}
	for _, l := range tui.Box("crash demo", intro, width, tui.Magenta) {
		fmt.Fprintln(w, l)
	}
	if !tui.IsTTY() {
		fmt.Fprintln(w)
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var results []roundResult
	total := 0
	for r := 1; r <= rounds; r++ {
		res, err := demoRound(w, kvdb, path, r, rounds, time.Duration(300+rng.Intn(800))*time.Millisecond)
		if err != nil {
			fmt.Fprintf(w, "%s %v\n", tui.Fail(), err)
			return false
		}
		total += res.onDisk
		res.keysTotal = total
		results = append(results, res)
	}
	return printDemoSummary(w, results, width)
}

func demoRound(w io.Writer, kvdb, path string, round, rounds int, run time.Duration) (roundResult, error) {
	cmd := exec.Command(kvdb, "serve", path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return roundResult{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return roundResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return roundResult{}, fmt.Errorf("cannot start %s: %w", kvdb, err)
	}
	var acks, sent int64
	ready := make(chan struct{})
	readerDone := make(chan struct{})
	go func() { // only COMPLETE lines count; a half-written last line is not an ACK
		defer close(readerDone)
		br := bufio.NewReaderSize(stdout, 1<<16)
		first := true
		for {
			line, err := br.ReadString('\n')
			if strings.HasSuffix(line, "\n") {
				switch {
				case first && strings.HasPrefix(line, "READY"):
					first = false
					close(ready)
				case strings.HasPrefix(line, "ACK "):
					atomic.AddInt64(&acks, 1)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		bw := bufio.NewWriterSize(stdin, 1<<16)
		for i := 0; ; i++ {
			if _, err := fmt.Fprintf(bw, "put r%d-%d value%d\n", round, i, i); err != nil {
				return
			}
			atomic.AddInt64(&sent, 1)
			if i%32 == 0 {
				if bw.Flush() != nil {
					return
				}
			}
		}
	}()

	select {
	case <-ready:
	case <-time.After(20 * time.Second):
		cmd.Process.Kill()
		return roundResult{}, fmt.Errorf("serve did not become ready")
	}

	start := time.Now()
	height, frame := 0, 0
	for time.Since(start) < run {
		n := atomic.LoadInt64(&acks)
		el := time.Since(start)
		line := fmt.Sprintf(" %s round %d/%d  writing…  %s ACKed  %s  %s",
			tui.Cyan(tui.SpinnerFrames[frame%len(tui.SpinnerFrames)]), round, rounds,
			tui.Bold(fmt.Sprintf("%6d", n)), tui.Bar(float64(el)/float64(run), 22), tui.Gray(fmt.Sprintf("%.0f/s", float64(n)/el.Seconds())))
		if tui.IsTTY() { // the animation needs cursor control; without a terminal only the results are printed
			height = tui.Redraw(w, height, []string{line})
		}
		frame++
		time.Sleep(50 * time.Millisecond)
	}
	killedAt := time.Since(start)
	_ = cmd.Process.Kill() // SIGKILL
	<-readerDone
	_ = cmd.Wait()
	n := atomic.LoadInt64(&acks)
	tui.Redraw(w, height, []string{fmt.Sprintf(" %s round %d/%d  %s after %s: %s ACKs were received", tui.Red("✸"), round, rounds, tui.Red(tui.Bold("SIGKILL")), killedAt.Round(time.Millisecond), tui.Bold(fmt.Sprint(n)))})

	// Open the crashed file in this process (recovery reads, it never writes) and count this round's keys.
	db, err := engine.Open(path, engine.Options{})
	if err != nil {
		return roundResult{}, fmt.Errorf("open after crash FAILED: %w", err)
	}
	st := db.Stats()
	prefix := fmt.Sprintf("r%d-", round)
	onDisk := 0
	_ = db.ForEach(func(k, v []byte) error {
		if strings.HasPrefix(string(k), prefix) {
			onDisk++
		}
		return nil
	})
	_ = db.Close()

	res := roundResult{round: round, after: killedAt, acks: n, onDisk: onDisk, tail: st.Tail, recovery: st.Recovery}
	switch {
	case int64(onDisk) == n:
		res.verdict, res.ok = "exactly the ACKed", true
	case int64(onDisk) == n+1:
		res.verdict, res.ok = "ACKed + 1 in flight", true
	case int64(onDisk) < n:
		res.verdict = fmt.Sprintf("LOST %d ACKed writes", n-int64(onDisk))
	default:
		res.verdict = fmt.Sprintf("%d unACKed writes present (max 1 allowed)", int64(onDisk)-n)
	}
	mark := tui.OK()
	if !res.ok {
		mark = tui.Fail()
	}
	fmt.Fprintf(w, "   %s %d ACKed → %d on disk (%s) · file tail %s · recovered in %s\n", mark, n, onDisk, res.verdict, tui.Bold(st.Tail), st.Recovery.Round(time.Microsecond))
	return res, nil
}

func printDemoSummary(w io.Writer, rs []roundResult, width int) bool {
	var lines []string
	lines = append(lines, tui.Bold(fmt.Sprintf("%-6s %-9s %7s %8s  %-14s %s", "round", "killed at", "ACKed", "on disk", "tail", "verdict")))
	allOK := true
	var totalAck int64
	for _, r := range rs {
		mark := tui.OK()
		v := tui.Green(r.verdict)
		if !r.ok {
			allOK, mark, v = false, tui.Fail(), tui.Red(r.verdict)
		}
		totalAck += r.acks
		lines = append(lines, fmt.Sprintf("%-6d %-9s %7d %8d  %-14s %s %s", r.round, r.after.Round(time.Millisecond), r.acks, r.onDisk, r.tail, mark, v))
	}
	for _, l := range tui.Box("result", lines, width, tui.Cyan) {
		fmt.Fprintln(w, l)
	}
	var verdict string
	if allOK {
		verdict = fmt.Sprintf("%s %s", tui.OK(), tui.Green(tui.Bold(fmt.Sprintf("%d kills, %d acknowledged writes, 0 lost.", len(rs), totalAck))))
	} else {
		verdict = fmt.Sprintf("%s %s", tui.Fail(), tui.Red(tui.Bold("the promise was broken: see the table")))
	}
	fmt.Fprintln(w, verdict)
	fmt.Fprintln(w, tui.Gray("   Why \"+1\"? The server durably writes a transaction, THEN sends its ACK. A kill between the two leaves one"))
	fmt.Fprintln(w, tui.Gray("   transaction on disk that was never acknowledged. That is allowed; two, or any missing ACKed write, is not."))
	return allOK
}

// cacheBase is a directory on a real disk when possible: /tmp is often RAM, where fsync is free.
func cacheBase() string {
	if d, err := os.UserCacheDir(); err == nil {
		if os.MkdirAll(d, 0o755) == nil {
			return d
		}
	}
	return os.TempDir()
}
