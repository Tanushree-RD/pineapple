package main

// `attack <db>`: HYDRA attacks YOUR data. While a live `kvdb serve` rewrites the records that are already in the
// database (and a few records of its own under the key prefix hydra:), HYDRA kills it with SIGKILL at a random moment,
// reopens the file, and proves two things:
//
//  1. the records that were there before are byte-for-byte unchanged, however many kills have landed;
//  2. every acknowledged operation is present, and what the kill cut off is a clean prefix of what was in flight
//     (nothing partly applied, nothing out of order).
//
// The original data is never lost or altered: every write to an existing record carries the record's own value, and a
// transaction that deletes and re-inserts a record is atomic, so a crash can only show the old value or the same
// value. Watch it live in the browser (/live in the leapfrog environment) while this runs.

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/chethan-9817/pineapple/src/internal/tui"
)

const attackPrefix = "hydra:"

type attackOp struct {
	kind byte // 'p' put, 'd' delete, 't' transaction (delete+put of an original key, plus a hydra key put)
	key  string
	val  string
	ts   []attackOp
}

type state map[string]string // raw key -> raw value

func (s state) clone() state {
	c := make(state, len(s))
	for k, v := range s {
		c[k] = v
	}
	return c
}

func (o attackOp) apply(s state) {
	switch o.kind {
	case 'p':
		s[o.key] = o.val
	case 'd':
		delete(s, o.key)
	case 't':
		for _, t := range o.ts {
			t.apply(s)
		}
	}
}

func decodeText(s string) string {
	if strings.HasPrefix(s, "hex:") {
		if b, err := hex.DecodeString(s[4:]); err == nil {
			return string(b)
		}
	}
	return s
}

func wire(raw string) string { return "hex:" + hex.EncodeToString([]byte(raw)) }

func (o attackOp) lines() []string {
	switch o.kind {
	case 'p':
		return []string{"put " + wire(o.key) + " " + wire(o.val)}
	case 'd':
		return []string{"del " + wire(o.key)}
	}
	ls := []string{"begin"}
	for _, t := range o.ts {
		ls = append(ls, t.lines()...)
	}
	return append(ls, "commit")
}

// parseDump turns `kvdb dump` output into a state.
func parseDump(out []byte) state {
	s := state{}
	for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if l == "" {
			continue
		}
		kv := strings.SplitN(l, "\t", 2)
		v := ""
		if len(kv) == 2 {
			v = kv[1]
		}
		s[decodeText(kv[0])] = decodeText(v)
	}
	return s
}

func dumpState(kvdb, db string) (state, error) {
	out, err := exec.Command(kvdb, "dump", db).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("kvdb dump failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return parseDump(out), nil
}

// verifyAfterKill checks the state read after a kill. acked is the state with every acknowledged operation applied;
// inflight are the operations sent but not acknowledged, in the order sent. The file must hold acked plus a PREFIX of
// inflight. It also checks that the original records are untouched. It returns "" or what is wrong.
func verifyAfterKill(original, acked state, inflight []attackOp, got state) string {
	for k, v := range original {
		g, ok := got[k]
		if !ok {
			return fmt.Sprintf("original record %q is MISSING after the kill", printable(k))
		}
		if g != v {
			return fmt.Sprintf("original record %q CHANGED: was %q, now %q", printable(k), trim(v), trim(g))
		}
	}
	cur := acked.clone()
	if eqState(cur, got) {
		return ""
	}
	for _, op := range inflight {
		op.apply(cur)
		if eqState(cur, got) {
			return ""
		}
	}
	return "the database is not (acknowledged operations + a prefix of the operations in flight): " + diffSummary(acked, got)
}

func eqState(a, b state) bool {
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

func diffSummary(want, got state) string {
	var msgs []string
	for k, v := range want {
		if w, ok := got[k]; !ok {
			msgs = append(msgs, fmt.Sprintf("acknowledged %q is missing", printable(k)))
		} else if w != v {
			msgs = append(msgs, fmt.Sprintf("%q is %q, expected %q", printable(k), trim(w), trim(v)))
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok && len(msgs) < 6 {
			msgs = append(msgs, fmt.Sprintf("unexpected record %q", printable(k)))
		}
	}
	sort.Strings(msgs)
	if len(msgs) > 4 {
		msgs = append(msgs[:4], "...")
	}
	return strings.Join(msgs, "; ")
}

func printable(s string) string {
	return trim(strings.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return '?'
		}
		return r
	}, s))
}
func trim(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// attackOps builds the endless stream of operations: rewrites of originals with their own value, atomic
// delete-and-reinsert transactions of an original, and puts/deletes of hydra: keys.
func attackOps(rng *rand.Rand, original state, round int) func(n int) attackOp {
	keys := make([]string, 0, len(original))
	for k := range original {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return func(n int) attackOp {
		hk := fmt.Sprintf("%s%d:%d", attackPrefix, round, rng.Intn(12))
		switch r := rng.Intn(10); {
		case r < 4 && len(keys) > 0:
			k := keys[rng.Intn(len(keys))]
			return attackOp{kind: 'p', key: k, val: original[k]}
		case r < 6 && len(keys) > 0:
			k := keys[rng.Intn(len(keys))]
			return attackOp{kind: 't', ts: []attackOp{{kind: 'd', key: k}, {kind: 'p', key: k, val: original[k]}, {kind: 'p', key: hk, val: fmt.Sprintf("attack-%d-%d", round, n)}}}
		case r < 9:
			return attackOp{kind: 'p', key: hk, val: fmt.Sprintf("attack-%d-%d", round, n)}
		default:
			return attackOp{kind: 'd', key: hk}
		}
	}
}

type attackResult struct {
	Rounds, Kills, Acked int
	Err                  string
}

// attack runs the rounds. It returns an error text in the result when anything is wrong.
func attack(kvdb, db string, rounds int, seed int64, out io.Writer) attackResult {
	var res attackResult
	st, err := os.Stat(db)
	if err != nil || st.IsDir() {
		res.Err = fmt.Sprintf("%s does not exist: create it first (in the leapfrog environment: /seed %s)", db, db)
		return res
	}
	if p, err := dumpState(kvdb, db); err != nil {
		res.Err = err.Error()
		return res
	} else if purge(kvdb, db, p) != nil {
		res.Err = "could not clear the leftovers of an earlier attack"
		return res
	}
	original, err := dumpState(kvdb, db)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	fmt.Fprintf(out, "  attacking %s: %d records are the target and must come out byte-identical\n", db, len(original))
	rng := rand.New(rand.NewSource(seed))
	current := original.clone()
	for r := 1; r <= rounds; r++ {
		next := attackOps(rng, original, r)
		acked, inflight, err := oneKill(kvdb, db, next, rng, current)
		if err != nil {
			res.Err = fmt.Sprintf("round %d: %v", r, err)
			return res
		}
		res.Rounds++
		res.Kills++
		res.Acked += len(acked.ops)
		got, err := dumpState(kvdb, db)
		if err != nil {
			res.Err = fmt.Sprintf("round %d: after the kill the database would not open: %v", r, err)
			return res
		}
		if msg := verifyAfterKill(original, acked.state, inflight, got); msg != "" {
			res.Err = fmt.Sprintf("round %d (seed %d): %s", r, seed, msg)
			return res
		}
		current = got
		hyd := 0
		for k := range got {
			if strings.HasPrefix(k, attackPrefix) {
				hyd++
			}
		}
		fmt.Fprintf(out, "  %s round %2d/%d  killed with %2d ACKed · %d original records intact · %d hydra records consistent\n", tui.OK(), r, rounds, len(acked.ops), len(original), hyd)
	}
	final, err := dumpState(kvdb, db)
	if err == nil {
		err = purge(kvdb, db, final)
	}
	if err != nil {
		res.Err = "cleanup failed: " + err.Error()
		return res
	}
	if after, err := dumpState(kvdb, db); err != nil || !eqState(after, original) {
		res.Err = "after removing the hydra: records the database is not identical to what it was before the attack"
	}
	return res
}

// purge deletes every hydra: record in one atomic batch.
func purge(kvdb, db string, s state) error {
	var b bytes.Buffer
	for k := range s {
		if strings.HasPrefix(k, attackPrefix) {
			fmt.Fprintf(&b, "del %s\n", wire(k))
		}
	}
	if b.Len() == 0 {
		return nil
	}
	f, err := os.CreateTemp("", "hydra-purge-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	f.Write(b.Bytes())
	f.Close()
	if out, err := exec.Command(kvdb, "batch", db, f.Name()).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

type ackedOps struct {
	ops   []attackOp
	state state
}

// oneKill starts a server, streams operations, SIGKILLs it at a random moment and returns what was acknowledged
// (applied, in order, on top of start) and what was still in flight.
func oneKill(kvdb, db string, next func(int) attackOp, rng *rand.Rand, start state) (ackedOps, []attackOp, error) {
	cmd := exec.Command(kvdb, "serve", db)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return ackedOps{}, nil, err
	}
	var sent []attackOp
	ackCh := make(chan int, 4096)
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		first := true
		for sc.Scan() {
			l := sc.Text()
			if first && strings.HasPrefix(l, "READY") {
				first = false
				close(ready)
				continue
			}
			var id int
			if n, _ := fmt.Sscanf(l, "ACK %d", &id); n == 1 {
				ackCh <- id
			}
		}
		close(ackCh)
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return ackedOps{}, nil, fmt.Errorf("the server did not become ready in 10 s")
	}
	killAt := time.Now().Add(time.Duration(15+rng.Intn(120)) * time.Millisecond)
	killed := make(chan struct{})
	go func() {
		time.Sleep(time.Until(killAt))
		_ = cmd.Process.Signal(syscall.SIGKILL)
		close(killed)
	}()
	w := bufio.NewWriter(stdin)
write:
	for n := 0; ; n++ {
		select {
		case <-killed:
			break write
		default:
		}
		op := next(n)
		sent = append(sent, op)
		for _, l := range op.lines() {
			w.WriteString(l + "\n")
		}
		if w.Flush() != nil {
			break
		}
		if n%3 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	<-killed
	_ = cmd.Wait()
	acks := map[int]bool{}
	for id := range ackCh {
		acks[id] = true
	}
	// ACK ids count operations and transactions in the order sent; the highest ACK id says how many were acknowledged
	// (the engine acknowledges in order).
	top := 0
	for id := range acks {
		if id > top {
			top = id
		}
	}
	if top > len(sent) {
		top = len(sent)
	}
	res := ackedOps{ops: sent[:top], state: start.clone()}
	for _, op := range res.ops {
		op.apply(res.state)
	}
	return res, sent[top:], nil
}
