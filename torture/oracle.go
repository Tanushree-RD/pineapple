package main

// The oracle: HYDRA's own, append-only record of what it asked the database to do and
// what the database told it (SPEC 14.1, 14.2, 14.5). The database's dump is only ever an
// observation; this log is the truth.
//
// Record formats (one per line, fields separated by single spaces, keys in lowercase hex):
//
//	ITER   <iter> <seed>
//	SENT   <id> <lane> <kind> <invoke_seq> <t_ns> <witness|-> <nparts> <part>...
//	ACK    <id> <event_seq> <ack_seq> <t_ns>
//	REJECT <id> <reason> <event_seq> <t_ns>
//	UNKNOWN <id> <event_seq> <t_ns>
//	KILL   <iter> <delay_ms>
//
// A part is "P:<keyhex>:<size>" (a PUT whose value has <size> bytes) or "D:<keyhex>" (a DELETE).
// <kind> is PUT, DEL, BATCH or COMPACT. Times are nanoseconds on a monotonic clock since the
// oracle was created. event_seq is one counter shared by SENT and ACK events, so "A was ACKed
// before B was invoked" is simply ack event_seq < invoke_seq. ack_seq is the position of the ACK
// among this iteration's ACKs, which serve prints in commit order (SPEC 14.5).

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OpKind is the request type.
type OpKind uint8

const (
	KindPut OpKind = iota + 1
	KindDel
	KindBatch
	KindCompact
)

func (k OpKind) String() string {
	switch k {
	case KindPut:
		return "PUT"
	case KindDel:
		return "DEL"
	case KindBatch:
		return "BATCH"
	case KindCompact:
		return "COMPACT"
	}
	return "?"
}

func parseKind(s string) (OpKind, bool) {
	switch s {
	case "PUT":
		return KindPut, true
	case "DEL":
		return KindDel, true
	case "BATCH":
		return KindBatch, true
	case "COMPACT":
		return KindCompact, true
	}
	return 0, false
}

// OpState is the lifecycle state of one request (SPEC 14.4).
type OpState uint8

const (
	// StateInFlight: sent, no reply (or ERR IO). May or may not be present after a kill; never partially.
	StateInFlight OpState = iota
	// StateAcked: a complete ACK line was read. Must be reflected unless a later op overrides it.
	StateAcked
	// StateRejected: ERR FULL / INVALID. Must never appear.
	StateRejected
	// StateDead: an op from an earlier iteration that is not the one observed. Must never appear again.
	StateDead
)

func (s OpState) String() string {
	switch s {
	case StateInFlight:
		return "IN_FLIGHT"
	case StateAcked:
		return "ACKED"
	case StateRejected:
		return "REJECTED"
	case StateDead:
		return "DEAD"
	}
	return "?"
}

// Part is one element of a transaction: a PUT of a value with Size bytes, or a DELETE.
type Part struct {
	Del  bool
	Key  []byte
	Size int // value length for a PUT (0 = explicit empty value); ignored for a DELETE
}

// Op is one request (one atomic transaction) and everything we know about its fate.
type Op struct {
	ID      uint64
	Lane    int
	Kind    OpKind
	Iter    int
	Parts   []Part // PUT/DEL: exactly one; BATCH: 1..10,000; COMPACT: none
	Witness []byte // for default-mode batches: a key every batch writes, so all-or-none is visible

	InvokeSeq uint64 // event counter value when the request line was written
	InvokeNS  int64
	AckEvent  uint64 // event counter value when the ACK line was read (0 = none)
	AckSeq    uint64 // 1-based position among this iteration's ACKs = commit order (0 = none)
	AckNS     int64

	State  OpState
	Reason string // for REJECTED: FULL / INVALID
}

// Oracle is the live recorder. It is safe for concurrent use.
type Oracle struct {
	mu       sync.Mutex
	f        *os.File
	start    time.Time
	event    uint64
	ackCount uint64
	iter     int
	ops      map[uint64]*Op // every op of the whole run
	cur      []*Op          // ops of the current iteration, in SENT order
}

// NewOracle creates (truncating) the append-only log at path.
func NewOracle(path string) (*Oracle, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Oracle{f: f, start: time.Now(), ops: make(map[uint64]*Op)}, nil
}

// Close closes the log file.
func (o *Oracle) Close() error { return o.f.Close() }

func (o *Oracle) now() int64 { return int64(time.Since(o.start)) }

// write appends one record. Caller holds o.mu. A single Write call keeps each record whole.
func (o *Oracle) write(format string, args ...any) {
	if _, err := fmt.Fprintf(o.f, format+"\n", args...); err != nil {
		// An oracle that cannot record is worse than none: stop loudly.
		fmt.Fprintf(os.Stderr, "hydra: cannot append to oracle log: %v\n", err)
		os.Exit(2)
	}
}

// BeginIteration starts a new iteration: ACK order restarts at 1.
func (o *Oracle) BeginIteration(iter int, seed uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.iter = iter
	o.ackCount = 0
	o.cur = nil
	o.write("ITER %d %d", iter, seed)
}

// Sent records a request. It MUST be called before the request line is written to serve's stdin
// (SPEC 14.2), so a kill can never leave a request that the oracle does not know about.
func (o *Oracle) Sent(op *Op) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.event++
	op.InvokeSeq = o.event
	op.InvokeNS = o.now()
	op.Iter = o.iter
	op.State = StateInFlight
	o.ops[op.ID] = op
	o.cur = append(o.cur, op)
	o.write("SENT %d %d %s %d %d %s %d%s", op.ID, op.Lane, op.Kind, op.InvokeSeq, op.InvokeNS,
		hexOrDash(op.Witness), len(op.Parts), formatParts(op.Parts))
}

// Ack records a complete "ACK <id>" line. ACKs arrive in commit order, so AckSeq is the commit order.
func (o *Oracle) Ack(id uint64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	op, ok := o.ops[id]
	if !ok {
		return fmt.Errorf("ACK for unknown op id %d", id)
	}
	if op.State == StateAcked {
		return fmt.Errorf("duplicate ACK for op id %d", id)
	}
	o.event++
	o.ackCount++
	op.State = StateAcked
	op.AckEvent = o.event
	op.AckSeq = o.ackCount
	op.AckNS = o.now()
	o.write("ACK %d %d %d %d", id, op.AckEvent, op.AckSeq, op.AckNS)
	return nil
}

// Reject records "ERR <id> FULL" / "ERR <id> INVALID": the op was definitely not applied.
func (o *Oracle) Reject(id uint64, reason string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	op, ok := o.ops[id]
	if !ok {
		return fmt.Errorf("ERR for unknown op id %d", id)
	}
	o.event++
	op.State = StateRejected
	op.Reason = reason
	o.write("REJECT %d %s %d %d", id, reason, o.event, o.now())
	return nil
}

// Unknown records "ERR <id> IO": the outcome is unknown, so the op stays IN_FLIGHT.
func (o *Oracle) Unknown(id uint64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.ops[id]; !ok {
		return fmt.Errorf("ERR IO for unknown op id %d", id)
	}
	o.event++
	o.write("UNKNOWN %d %d %d", id, o.event, o.now())
	return nil
}

// Kill records the kill that ended the iteration.
func (o *Oracle) Kill(iter int, delayMS int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.write("KILL %d %d", iter, delayMS)
}

// Op returns the op with this id, if any.
func (o *Oracle) Op(id uint64) (*Op, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	op, ok := o.ops[id]
	return op, ok
}

// Iteration returns the current iteration's ops in SENT order. Call it only after the lanes and
// the reply reader have stopped.
func (o *Oracle) Iteration() []*Op {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]*Op(nil), o.cur...)
}

// Retire marks every op of the current iteration that the checker did not accept as the observed
// state as DEAD (SPEC 14.4 step 3). REJECTED ops stay REJECTED so they can never be mistaken for dead.
func (o *Oracle) Retire(observedIDs map[uint64]bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, op := range o.cur {
		if op.State == StateRejected || observedIDs[op.ID] {
			continue
		}
		op.State = StateDead
	}
}

// StateOf reports the recorded state of an op id from any iteration.
func (o *Oracle) StateOf(id uint64) (OpState, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	op, ok := o.ops[id]
	if !ok {
		return 0, false
	}
	return op.State, true
}

func hexOrDash(b []byte) string {
	if len(b) == 0 {
		return "-"
	}
	return hex.EncodeToString(b)
}

func formatParts(parts []Part) string {
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteByte(' ')
		if p.Del {
			sb.WriteString("D:" + hex.EncodeToString(p.Key))
		} else {
			sb.WriteString("P:" + hex.EncodeToString(p.Key) + ":" + strconv.Itoa(p.Size))
		}
	}
	return sb.String()
}

// ---------------------------------------------------------------------------------------------
// Reading a log back (for --replay).

// LoggedIteration is one iteration reconstructed from oracle.log.
type LoggedIteration struct {
	Iter    int
	Seed    uint64
	KillMS  int64
	Ops     []*Op // in SENT order
	byID    map[uint64]*Op
	HasKill bool
}

// ParseOracleLog rebuilds iterations from an oracle.log. Unknown record types are an error: a log
// we cannot fully understand must not be silently accepted as a clean replay.
func ParseOracleLog(r io.Reader) ([]*LoggedIteration, error) {
	var its []*LoggedIteration
	var cur *LoggedIteration
	br := bufio.NewReaderSize(r, 1<<20)
	lineNo := 0
	for {
		line, err := br.ReadString('\n')
		if len(line) == 0 && err == io.EOF {
			break
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		lineNo++
		line = strings.TrimRight(line, "\n")
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		bad := func(msg string) error {
			return fmt.Errorf("oracle.log line %d: %s: %q", lineNo, msg, truncate(line, 120))
		}
		switch f[0] {
		case "ITER":
			if len(f) != 3 {
				return nil, bad("ITER needs 2 fields")
			}
			n, e1 := strconv.Atoi(f[1])
			seed, e2 := strconv.ParseUint(f[2], 10, 64)
			if e1 != nil || e2 != nil {
				return nil, bad("bad ITER numbers")
			}
			cur = &LoggedIteration{Iter: n, Seed: seed, byID: map[uint64]*Op{}}
			its = append(its, cur)
		case "SENT":
			if cur == nil {
				return nil, bad("SENT before ITER")
			}
			op, e := parseSent(f, cur.Iter)
			if e != nil {
				return nil, bad(e.Error())
			}
			cur.Ops = append(cur.Ops, op)
			cur.byID[op.ID] = op
		case "ACK", "REJECT", "UNKNOWN":
			if cur == nil {
				return nil, bad(f[0] + " before ITER")
			}
			if err := applyReply(cur, f); err != nil {
				return nil, bad(err.Error())
			}
		case "KILL":
			if cur == nil || len(f) != 3 {
				return nil, bad("bad KILL")
			}
			ms, e := strconv.ParseInt(f[2], 10, 64)
			if e != nil {
				return nil, bad("bad KILL delay")
			}
			cur.KillMS, cur.HasKill = ms, true
		default:
			return nil, bad("unknown record type")
		}
		if err == io.EOF {
			break
		}
	}
	return its, nil
}

func parseSent(f []string, iter int) (*Op, error) {
	// SENT id lane kind invoke_seq t_ns witness nparts part...
	if len(f) < 8 {
		return nil, fmt.Errorf("SENT needs at least 7 fields")
	}
	id, e1 := strconv.ParseUint(f[1], 10, 64)
	lane, e2 := strconv.Atoi(f[2])
	kind, ok := parseKind(f[3])
	iseq, e3 := strconv.ParseUint(f[4], 10, 64)
	ins, e4 := strconv.ParseInt(f[5], 10, 64)
	np, e5 := strconv.Atoi(f[7])
	if e1 != nil || e2 != nil || !ok || e3 != nil || e4 != nil || e5 != nil || id == 0 || np < 0 {
		return nil, fmt.Errorf("bad SENT header")
	}
	if len(f) != 8+np {
		return nil, fmt.Errorf("SENT declares %d parts but has %d", np, len(f)-8)
	}
	op := &Op{ID: id, Lane: lane, Kind: kind, Iter: iter, InvokeSeq: iseq, InvokeNS: ins, State: StateInFlight}
	if f[6] != "-" {
		w, err := hex.DecodeString(f[6])
		if err != nil {
			return nil, fmt.Errorf("bad witness hex")
		}
		op.Witness = w
	}
	for _, tok := range f[8:] {
		p, err := parsePart(tok)
		if err != nil {
			return nil, err
		}
		op.Parts = append(op.Parts, p)
	}
	return op, nil
}

func parsePart(tok string) (Part, error) {
	switch {
	case strings.HasPrefix(tok, "D:"):
		k, err := hex.DecodeString(tok[2:])
		if err != nil || len(k) == 0 {
			return Part{}, fmt.Errorf("bad DELETE part %q", tok)
		}
		return Part{Del: true, Key: k}, nil
	case strings.HasPrefix(tok, "P:"):
		rest := strings.SplitN(tok[2:], ":", 2)
		if len(rest) != 2 {
			return Part{}, fmt.Errorf("bad PUT part %q", tok)
		}
		k, err := hex.DecodeString(rest[0])
		size, err2 := strconv.Atoi(rest[1])
		if err != nil || err2 != nil || len(k) == 0 || size < 0 {
			return Part{}, fmt.Errorf("bad PUT part %q", tok)
		}
		return Part{Key: k, Size: size}, nil
	}
	return Part{}, fmt.Errorf("bad part %q", tok)
}

func applyReply(it *LoggedIteration, f []string) error {
	id, err := strconv.ParseUint(f[1], 10, 64)
	if err != nil {
		return fmt.Errorf("bad op id")
	}
	op, ok := it.byID[id]
	if !ok {
		return fmt.Errorf("%s for op %d not sent in this iteration", f[0], id)
	}
	switch f[0] {
	case "ACK": // ACK id event_seq ack_seq t_ns
		if len(f) != 5 {
			return fmt.Errorf("ACK needs 4 fields")
		}
		ev, e1 := strconv.ParseUint(f[2], 10, 64)
		as, e2 := strconv.ParseUint(f[3], 10, 64)
		ns, e3 := strconv.ParseInt(f[4], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil {
			return fmt.Errorf("bad ACK numbers")
		}
		op.State, op.AckEvent, op.AckSeq, op.AckNS = StateAcked, ev, as, ns
	case "REJECT": // REJECT id reason event_seq t_ns
		if len(f) != 5 {
			return fmt.Errorf("REJECT needs 4 fields")
		}
		op.State, op.Reason = StateRejected, f[2]
	case "UNKNOWN":
		if len(f) != 4 {
			return fmt.Errorf("UNKNOWN needs 3 fields")
		}
		// stays IN_FLIGHT
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
