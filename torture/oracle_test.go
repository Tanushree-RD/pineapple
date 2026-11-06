package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestOracle(t *testing.T) (*Oracle, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oracle.log")
	o, err := NewOracle(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	return o, path
}

func TestOracleLifecycle(t *testing.T) {
	o, _ := newTestOracle(t)
	o.BeginIteration(1, 99)

	a := &Op{ID: 1, Lane: 0, Kind: KindPut, Parts: []Part{{Key: []byte("a"), Size: 64}}}
	b := &Op{ID: 2, Lane: 1, Kind: KindDel, Parts: []Part{{Del: true, Key: []byte("b")}}}
	c := &Op{ID: 3, Lane: 1, Kind: KindPut, Parts: []Part{{Key: []byte("c"), Size: 64}}}
	d := &Op{ID: 4, Lane: 0, Kind: KindPut, Parts: []Part{{Key: []byte("d"), Size: 64}}}
	for _, op := range []*Op{a, b, c, d} {
		o.Sent(op)
	}
	if !(a.InvokeSeq < b.InvokeSeq && b.InvokeSeq < c.InvokeSeq) {
		t.Fatalf("invoke_seq must strictly increase: %d %d %d", a.InvokeSeq, b.InvokeSeq, c.InvokeSeq)
	}

	// ACKs arrive in commit order: c first, then a.
	if err := o.Ack(3); err != nil {
		t.Fatal(err)
	}
	if err := o.Ack(1); err != nil {
		t.Fatal(err)
	}
	if c.AckSeq != 1 || a.AckSeq != 2 {
		t.Fatalf("ack_seq must follow arrival order: c=%d a=%d", c.AckSeq, a.AckSeq)
	}
	if c.State != StateAcked || a.State != StateAcked {
		t.Fatal("acked ops must be ACKED")
	}
	// Real time: b was invoked before c was acked, a was acked after b was invoked.
	if !(b.InvokeSeq < c.AckEvent) {
		t.Fatal("event counter must be shared by SENT and ACK")
	}

	if err := o.Reject(2, "FULL"); err != nil {
		t.Fatal(err)
	}
	if b.State != StateRejected || b.Reason != "FULL" {
		t.Fatalf("b = %v %q", b.State, b.Reason)
	}
	if err := o.Unknown(4); err != nil {
		t.Fatal(err)
	}
	if d.State != StateInFlight {
		t.Fatalf("UNKNOWN must leave the op IN_FLIGHT, got %v", d.State)
	}

	if err := o.Ack(3); err == nil {
		t.Fatal("duplicate ACK must be an error")
	}
	if err := o.Ack(999); err == nil {
		t.Fatal("ACK for an unknown id must be an error")
	}

	o.Retire(map[uint64]bool{1: true})
	if a.State != StateAcked {
		t.Fatal("an observed op must not be retired")
	}
	if c.State != StateDead || d.State != StateDead {
		t.Fatalf("unobserved ops must become DEAD: c=%v d=%v", c.State, d.State)
	}
	if b.State != StateRejected {
		t.Fatal("REJECTED must stay REJECTED")
	}
}

func TestOracleAckOrderRestartsPerIteration(t *testing.T) {
	o, _ := newTestOracle(t)
	o.BeginIteration(1, 1)
	x := &Op{ID: 1, Kind: KindPut, Parts: []Part{{Key: []byte("x"), Size: 16}}}
	o.Sent(x)
	_ = o.Ack(1)
	o.BeginIteration(2, 2)
	y := &Op{ID: 2, Kind: KindPut, Parts: []Part{{Key: []byte("y"), Size: 16}}}
	o.Sent(y)
	_ = o.Ack(2)
	if x.AckSeq != 1 || y.AckSeq != 1 {
		t.Fatalf("ack_seq restarts at 1 each iteration: %d %d", x.AckSeq, y.AckSeq)
	}
	if got := o.Iteration(); len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("Iteration() must return only the current iteration, got %d ops", len(got))
	}
}

func TestOracleLogRoundTrip(t *testing.T) {
	o, path := newTestOracle(t)
	o.BeginIteration(1, 12345)
	put := &Op{ID: 10, Lane: 2, Kind: KindPut, Parts: []Part{{Key: []byte{0x00, 0xff, 'k'}, Size: 1 << 20}}}
	batch := &Op{ID: 11, Lane: 0, Kind: KindBatch, Witness: []byte("w:11"), Parts: []Part{
		{Key: []byte("b1"), Size: 40}, {Del: true, Key: []byte("b2")}, {Key: []byte("w:11"), Size: 12},
	}}
	cmp := &Op{ID: 12, Lane: 3, Kind: KindCompact}
	lost := &Op{ID: 13, Lane: 1, Kind: KindPut, Parts: []Part{{Key: []byte("z"), Size: 0}}}
	for _, op := range []*Op{put, batch, cmp, lost} {
		o.Sent(op)
	}
	_ = o.Ack(10)
	_ = o.Ack(11)
	_ = o.Reject(12, "FULL")
	_ = o.Unknown(13)
	o.Kill(1, 134)
	o.Observed(map[string]KeyState{
		"a\x00": {Kind: KeyValue, OpID: 10, Size: 64},
		"b":     {},
		"c":     {Kind: KeyEmpty},
	})
	o.BeginIteration(2, 777)
	next := &Op{ID: 14, Kind: KindDel, Parts: []Part{{Del: true, Key: []byte("q")}}}
	o.Sent(next)

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	its, err := ParseOracleLog(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(its) != 2 || its[0].Iter != 1 || its[0].Seed != 12345 || its[1].Seed != 777 {
		t.Fatalf("iterations: %+v", its)
	}
	if !its[0].HasKill || its[0].KillMS != 134 {
		t.Fatalf("kill not restored: %+v", its[0])
	}
	got := its[0].Ops
	if len(got) != 4 {
		t.Fatalf("want 4 ops, got %d", len(got))
	}
	if got[0].State != StateAcked || got[0].AckSeq != 1 || got[1].AckSeq != 2 {
		t.Fatalf("ack info lost: %+v %+v", got[0], got[1])
	}
	if got[2].State != StateRejected || got[2].Reason != "FULL" {
		t.Fatalf("reject lost: %+v", got[2])
	}
	if got[3].State != StateInFlight {
		t.Fatalf("UNKNOWN must restore as IN_FLIGHT: %v", got[3].State)
	}
	if string(got[0].Parts[0].Key) != "\x00\xffk" || got[0].Parts[0].Size != 1<<20 {
		t.Fatalf("binary key or size lost: %+v", got[0].Parts[0])
	}
	if string(got[1].Witness) != "w:11" || len(got[1].Parts) != 3 || !got[1].Parts[1].Del {
		t.Fatalf("batch lost: %+v", got[1])
	}
	if got[0].InvokeSeq != put.InvokeSeq || got[1].AckEvent != batch.AckEvent {
		t.Fatal("event counters must survive the round trip")
	}
	obs := its[0].Obs
	if len(obs) != 3 || obs["a\x00"].OpID != 10 || obs["a\x00"].Size != 64 || obs["b"].Kind != KeyAbsent || obs["c"].Kind != KeyEmpty {
		t.Fatalf("OBS records lost: %+v", obs)
	}
	if len(its[1].Ops) != 1 || its[1].Ops[0].Kind != KindDel {
		t.Fatalf("second iteration: %+v", its[1])
	}
}

func TestParseOracleLogRejectsGarbage(t *testing.T) {
	cases := map[string]string{
		"unknown record":   "ITER 1 1\nBOGUS 1\n",
		"sent before iter": "SENT 1 0 PUT 1 1 - 1 P:6b:16\n",
		"part count":       "ITER 1 1\nSENT 1 0 PUT 1 1 - 2 P:6b:16\n",
		"bad part":         "ITER 1 1\nSENT 1 0 PUT 1 1 - 1 X:6b\n",
		"bad kind":         "ITER 1 1\nSENT 1 0 NOPE 1 1 - 1 P:6b:16\n",
		"ack unknown op":   "ITER 1 1\nACK 9 1 1 1\n",
		"zero id":          "ITER 1 1\nSENT 0 0 PUT 1 1 - 1 P:6b:16\n",
		"bad obs state":    "ITER 1 1\nOBS 6b V:1\n",
		"obs before iter":  "OBS 6b -\n",
	}
	for name, text := range cases {
		if _, err := ParseOracleLog(strings.NewReader(text)); err == nil {
			t.Errorf("%s: garbage accepted", name)
		}
	}
}
