package main

import (
	"strings"
	"testing"
)

// scenario builds an iteration by hand: ops with explicit states, a baseline, and a dump.
type scenario struct {
	t     *testing.T
	ops   []*Op
	base  Baseline
	dump  map[string][]byte
	event uint64
	acks  uint64
	old   map[uint64]OpState // ops from earlier iterations
}

func newScenario(t *testing.T) *scenario {
	return &scenario{t: t, base: Baseline{}, dump: map[string][]byte{}, old: map[uint64]OpState{}}
}

func (s *scenario) add(id uint64, kind OpKind, parts []Part, state OpState) *Op {
	s.event++
	op := &Op{ID: id, Kind: kind, Parts: parts, InvokeSeq: s.event, State: state}
	if state == StateAcked {
		s.event++
		s.acks++
		op.AckEvent, op.AckSeq = s.event, s.acks
	}
	s.ops = append(s.ops, op)
	return op
}

func (s *scenario) put(id uint64, key string, size int, state OpState) *Op {
	return s.add(id, KindPut, []Part{{Key: []byte(key), Size: size}}, state)
}

func (s *scenario) del(id uint64, key string, state OpState) *Op {
	return s.add(id, KindDel, []Part{{Del: true, Key: []byte(key)}}, state)
}

func (s *scenario) batch(id uint64, state OpState, parts ...Part) *Op {
	return s.add(id, KindBatch, parts, state)
}

func pp(key string, size int) Part { return Part{Key: []byte(key), Size: size} }
func dd(key string) Part           { return Part{Del: true, Key: []byte(key)} }

// present puts the value written by op into the dump (and as baseline when asBase).
func (s *scenario) present(key string, opID uint64, size int) {
	s.dump[key] = EncodeValue([]byte(key), opID, 0, size)
}

func (s *scenario) baseline(key string, opID uint64, size int) {
	s.base[key] = KeyState{Kind: KeyValue, OpID: opID, Size: size}
	s.old[opID] = StateDead
}

func (s *scenario) check() *CheckResult {
	return CheckIteration(s.base, s.ops, s.dump, func(id uint64) (OpState, bool) {
		st, ok := s.old[id]
		return st, ok
	})
}

func (s *scenario) wantPass() *CheckResult {
	s.t.Helper()
	r := s.check()
	if !r.OK() {
		s.t.Fatalf("expected PASS, got:\n%s", failureText(r))
	}
	return r
}

func (s *scenario) wantFail(substr string) *CheckResult {
	s.t.Helper()
	r := s.check()
	if r.OK() {
		s.t.Fatalf("expected FAIL containing %q, but the checker passed", substr)
	}
	if !strings.Contains(failureText(r), substr) {
		s.t.Fatalf("expected failure containing %q, got:\n%s", substr, failureText(r))
	}
	return r
}

func failureText(r *CheckResult) string {
	var sb strings.Builder
	for _, f := range r.Failures {
		sb.WriteString(f.String() + "\n")
	}
	return sb.String()
}

// ------------------------------------------------------------------ basic durability

func TestAckedPutPresent(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 64, StateAcked)
	s.present("a", 1, 64)
	s.wantPass()
}

func TestAckedPutLost(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 64, StateAcked)
	s.wantFail("ACKed write LOST")
}

func TestAckedDeleteResurrected(t *testing.T) {
	s := newScenario(t)
	s.baseline("a", 7, 64)
	s.del(1, "a", StateAcked)
	s.present("a", 7, 64) // the old value came back
	s.wantFail("op 7")
}

func TestAckedDeleteApplied(t *testing.T) {
	s := newScenario(t)
	s.baseline("a", 7, 64)
	s.del(1, "a", StateAcked)
	s.wantPass()
}

func TestBaselineKeyVanishes(t *testing.T) {
	s := newScenario(t)
	s.baseline("a", 7, 64)
	s.wantFail("durable data LOST")
}

func TestBaselineKeyUntouchedStays(t *testing.T) {
	s := newScenario(t)
	s.baseline("a", 7, 64)
	s.present("a", 7, 64)
	s.wantPass()
}

func TestUnknownKeyAppears(t *testing.T) {
	s := newScenario(t)
	s.present("ghost", 99, 64)
	s.wantFail("never sent")
}

func TestEmptyValueAcked(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 0, StateAcked)
	s.dump["a"] = []byte{}
	s.wantPass()
}

func TestEmptyValueIsNotDelete(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 0, StateAcked)
	s.wantFail("LOST") // absent where an empty value was ACKed
}

// ------------------------------------------------------------------ commit order (14.5)

func TestLaterAckedPutWins(t *testing.T) {
	s := newScenario(t)
	s.put(1, "k", 64, StateAcked)
	s.put(2, "k", 64, StateAcked)
	s.present("k", 2, 64)
	s.wantPass()
}

func TestEarlierAckedPutWinningIsAFailure(t *testing.T) {
	s := newScenario(t)
	s.put(1, "k", 64, StateAcked)
	s.put(2, "k", 64, StateAcked)
	s.present("k", 1, 64) // op 2 was acked later, so it must win
	s.wantFail("commit order violated")
}

func TestAckOrderNotSendOrder(t *testing.T) {
	// Op 5 is sent before op 4 but acknowledged after it. ACK order is commit order: 5 must win.
	s := newScenario(t)
	a := s.put(5, "k", 64, StateInFlight)
	b := s.put(4, "k", 64, StateAcked)
	a.State, s.acks = StateAcked, s.acks+1
	s.event++
	a.AckEvent, a.AckSeq = s.event, s.acks
	_ = b
	s.present("k", 5, 64)
	s.wantPass()
}

func TestAckedDeleteThenPutOrder(t *testing.T) {
	s := newScenario(t)
	s.put(1, "k", 64, StateAcked)
	s.del(2, "k", StateAcked)
	s.wantPass() // absent is the right answer
	s.present("k", 1, 64)
	s.wantFail("commit order violated")
}

// ------------------------------------------------------------------ unACKed transactions

func TestInFlightPutAbsentOrPresent(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 64, StateInFlight)
	s.wantPass() // absent: fine
	s.present("a", 1, 64)
	r := s.wantPass() // present: fine, it is the one surprise
	if r.Surprise == nil || r.Surprise.ID != 1 {
		t.Fatalf("surprise = %+v", r.Surprise)
	}
}

func TestTwoInFlightPresentIsAFailure(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 64, StateInFlight)
	s.put(2, "b", 64, StateInFlight)
	s.present("a", 1, 64)
	s.present("b", 2, 64)
	s.wantFail("more than one unACKed transaction")
}

func TestOneOfTwoInFlightPresent(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 64, StateInFlight)
	s.put(2, "b", 64, StateInFlight)
	s.present("b", 2, 64)
	s.wantPass()
}

func TestInFlightOverridesEarlierAckOnSameKey(t *testing.T) {
	s := newScenario(t)
	s.put(1, "k", 64, StateAcked)
	s.put(2, "k", 64, StateInFlight)
	s.present("k", 2, 64) // the unACKed op is last in commit order, so it wins
	s.wantPass()
	s.present("k", 1, 64) // or it never committed
	s.wantPass()
}

func TestInFlightDeleteCountsAsSurprise(t *testing.T) {
	s := newScenario(t)
	s.baseline("a", 7, 64)
	s.del(1, "a", StateInFlight)
	s.put(2, "b", 64, StateInFlight)
	s.wantPass() // a still present, b absent: nothing committed
	delete(s.dump, "a")
	s.wantPass() // only the delete committed
	s.present("b", 2, 64)
	s.wantFail("more than one unACKed transaction") // delete AND put committed
}

func TestRejectedMustNeverAppear(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 64, StateRejected)
	s.ops[0].Reason = "FULL"
	s.wantPass()
	s.present("a", 1, 64)
	s.wantFail("REJECTED op 1")
}

func TestDeadOpResurfacing(t *testing.T) {
	s := newScenario(t)
	s.old[3] = StateDead // from an earlier iteration
	s.baseline("a", 7, 64)
	s.present("a", 3, 64)
	s.wantFail("DEAD op 3 resurfaced")
}

// ------------------------------------------------------------------ batches (atomicity)

func TestBatchAckedAllPresent(t *testing.T) {
	s := newScenario(t)
	s.batch(1, StateAcked, pp("b1", 64), pp("b2", 64), pp("w", 40))
	s.present("b1", 1, 64)
	s.present("b2", 1, 64)
	s.present("w", 1, 40)
	s.wantPass()
}

func TestBatchAckedOneKeyMissing(t *testing.T) {
	s := newScenario(t)
	s.batch(1, StateAcked, pp("b1", 64), pp("b2", 64), pp("w", 40))
	s.present("b1", 1, 64)
	s.present("w", 1, 40)
	s.wantFail("LOST")
}

func TestBatchInFlightAllOrNone(t *testing.T) {
	s := newScenario(t)
	b := s.batch(1, StateInFlight, pp("b1", 64), pp("b2", 64), pp("w", 40))
	b.Witness = []byte("w")
	s.wantPass() // none
	s.present("b1", 1, 64)
	s.present("b2", 1, 64)
	s.present("w", 1, 40)
	s.wantPass() // all
}

func TestBatchInFlightPartialIsAFailure(t *testing.T) {
	s := newScenario(t)
	b := s.batch(1, StateInFlight, pp("b1", 64), pp("b2", 64), pp("w", 40))
	b.Witness = []byte("w")
	s.present("b1", 1, 64)
	s.present("w", 1, 40) // b2 missing
	r := s.wantFail("batch atomicity violated")
	if !strings.Contains(failureText(r), "witness key") {
		t.Fatalf("failure should mention the witness: %s", failureText(r))
	}
}

func TestBatchPutThenDeleteSameKey_B5(t *testing.T) {
	s := newScenario(t)
	s.batch(1, StateAcked, pp("x", 64), dd("x"))
	s.wantPass() // PUT X; DEL X leaves X absent
	s.present("x", 1, 64)
	s.wantFail("op 1")
}

func TestBatchDeleteThenPutSameKey_B5(t *testing.T) {
	s := newScenario(t)
	s.baseline("x", 7, 64)
	s.batch(1, StateAcked, dd("x"), pp("x", 64))
	s.present("x", 1, 64)
	s.wantPass() // DEL X; PUT X leaves X present
	delete(s.dump, "x")
	s.wantFail("LOST")
}

func TestMixedBatchPutAndDelete(t *testing.T) {
	s := newScenario(t)
	s.baseline("gone", 7, 64)
	s.batch(1, StateAcked, pp("new", 64), dd("gone"))
	s.present("new", 1, 64)
	s.wantPass()
	s.present("gone", 7, 64)
	s.wantFail("op 7")
}

// ------------------------------------------------------------------ value integrity

func TestGarbageValue(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 64, StateAcked)
	v := EncodeValue([]byte("a"), 1, 0, 64)
	v[40] ^= 0xff
	s.dump["a"] = v
	s.wantFail("fails verification")
}

func TestValueFromAnotherKey(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 64, StateAcked)
	s.put(2, "b", 64, StateAcked)
	s.dump["a"] = EncodeValue([]byte("b"), 2, 0, 64) // b's value stored under a
	s.present("b", 2, 64)
	s.wantFail("another key")
}

func TestTruncatedShortValueCaughtBySize(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 20, StateAcked)
	s.dump["a"] = EncodeValue([]byte("a"), 1, 0, 20)[:15] // short form decodes, but Size is wrong
	s.wantFail("truncated or extended")
}

func TestFlippedOpIDInShortValueIsRejected(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 12, StateAcked)
	v := EncodeValue([]byte("a"), 1, 0, 12)
	v[0] ^= 0x01 // a 12-byte value cannot detect this itself; the oracle must
	s.dump["a"] = v
	s.wantFail("never sent")
}

// ------------------------------------------------------------------ oracle sanity and bookkeeping

func TestOracleSanityAckBeforeInvoke(t *testing.T) {
	s := newScenario(t)
	op := s.put(1, "a", 64, StateAcked)
	op.AckEvent = op.InvokeSeq - 1
	s.present("a", 1, 64)
	s.wantFail("before it was invoked")
}

func TestBaselineUpdate(t *testing.T) {
	s := newScenario(t)
	s.baseline("gone", 7, 64)
	s.baseline("kept", 8, 64)
	s.put(1, "new", 64, StateAcked)
	s.del(2, "gone", StateAcked)
	s.present("kept", 8, 64)
	s.present("new", 1, 64)
	r := s.wantPass()
	UpdateBaseline(s.base, r)
	if _, ok := s.base["gone"]; ok {
		t.Fatal("deleted key must leave the baseline")
	}
	if s.base["new"].OpID != 1 || s.base["kept"].OpID != 8 {
		t.Fatalf("baseline wrong: %+v", s.base)
	}
	if !r.SeenOps[1] || !r.SeenOps[8] || r.SeenOps[2] {
		t.Fatalf("SeenOps wrong: %+v", r.SeenOps)
	}
}

func TestCounts(t *testing.T) {
	s := newScenario(t)
	s.put(1, "a", 64, StateAcked)
	s.put(2, "b", 64, StateInFlight)
	s.put(3, "c", 64, StateRejected)
	s.present("a", 1, 64)
	r := s.wantPass()
	if r.Acked != 1 || r.InFlight != 1 || r.Rejected != 1 {
		t.Fatalf("counts: %+v", r)
	}
}
