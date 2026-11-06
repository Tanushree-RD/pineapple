package main

// Workload generation and request encoding for default mode (SPEC 15.2, 15.3).
//
// Everything a lane does is a pure function of (master seed, iteration, lane): op kinds, keys,
// value sizes and even op ids. So a seed reproduces the workload exactly. What it cannot
// reproduce is OS scheduling or the instruction a real SIGKILL lands on (SPEC 15.3 "seed honesty").

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// rng is a splitmix64 stream.
type rng struct{ state uint64 }

func newRNG(seed uint64) *rng { return &rng{state: seed} }

func (r *rng) next() uint64 {
	v := splitmix64(r.state)
	r.state += 0x9e3779b97f4a7c15
	return v
}

// intn returns a value in [0, n). n must be > 0.
func (r *rng) intn(n int) int { return int(r.next() % uint64(n)) }

// between returns a value in [lo, hi].
func (r *rng) between(lo, hi int) int { return lo + r.intn(hi-lo+1) }

// iterSeed and laneSeed are the derivations named in SPEC 15.3.
func iterSeed(master uint64, iter int) uint64   { return splitmix64(master + uint64(iter)) }
func laneSeed(iterSeed uint64, lane int) uint64 { return splitmix64(iterSeed + uint64(lane)) }

// killDelayMS is the seeded kill delay: 10 + (rng mod 241) ms, i.e. 10..250 ms (SPEC 15.3).
func killDelayMS(iterSeed uint64) int { return 10 + int(splitmix64(iterSeed^0xdeadbeefcafef00d)%241) }

// opID packs (iteration, lane, sequence) so ids are unique across the whole run, deterministic,
// and never 0. iter < 2^27, lane < 16, seq < 2^32.
func opID(iter, lane int, seq uint32) uint64 {
	return uint64(iter)<<36 | uint64(lane)<<32 | uint64(seq)
}

// WorkloadParams shapes a generator.
type WorkloadParams struct {
	KeySpace  int // size of the shared ordinary keyspace (about 20k)
	HotKeys   int // size of the shared hot set that all lanes fight over (D2)
	MaxValue  int // upper bound of the common value-size range
	BatchMin  int
	BatchMax  int
	BatchSlot int // batch slots per lane; batch keys are recycled inside a lane-private range
}

// DefaultParams are the default-mode parameters.
func DefaultParams() WorkloadParams {
	return WorkloadParams{KeySpace: 20000, HotKeys: 32, MaxValue: 256, BatchMin: 2, BatchMax: 8, BatchSlot: 128}
}

// Generator produces one lane's ops.
type Generator struct {
	p    WorkloadParams
	iter int
	lane int
	r    *rng
	seq  uint32
}

// NewGenerator builds the generator for (master seed, iteration, lane).
func NewGenerator(p WorkloadParams, master uint64, iter, lane int) *Generator {
	return &Generator{p: p, iter: iter, lane: lane, r: newRNG(laneSeed(iterSeed(master, iter), lane))}
}

func (g *Generator) normalKey() []byte { return []byte(fmt.Sprintf("k%05d", g.r.intn(g.p.KeySpace))) }
func (g *Generator) hotKey() []byte    { return []byte(fmt.Sprintf("hot%02d", g.r.intn(g.p.HotKeys))) }

func (g *Generator) anyKey() []byte {
	if g.r.intn(4) == 0 {
		return g.hotKey()
	}
	return g.normalKey()
}

// valueSize picks a legal size: mostly the long form, some short-form (12..31), a few empty.
// allowEmpty is false where an empty value would be ambiguous (witness keys).
func (g *Generator) valueSize(allowEmpty bool) int {
	switch x := g.r.intn(100); {
	case x < 4 && allowEmpty:
		return 0
	case x < 14:
		return g.r.between(valueShortMin, valueHeaderLen-1)
	case x < 17:
		return g.r.between(valueHeaderLen, 4096)
	default:
		return g.r.between(valueHeaderLen, g.p.MaxValue)
	}
}

// Next returns the lane's next op. The id is assigned here; the oracle stamps the invoke time.
func (g *Generator) Next() *Op {
	g.seq++
	id := opID(g.iter, g.lane, g.seq)
	op := &Op{ID: id, Lane: g.lane}
	switch x := g.r.intn(100); {
	case x < 30: // PUT on an ordinary key
		op.Kind = KindPut
		op.Parts = []Part{{Key: g.normalKey(), Size: g.valueSize(true)}}
	case x < 45: // PUT on a hot key (many lanes fight over it)
		op.Kind = KindPut
		op.Parts = []Part{{Key: g.hotKey(), Size: g.valueSize(true)}}
	case x < 60: // DELETE
		op.Kind = KindDel
		op.Parts = []Part{{Del: true, Key: g.anyKey()}}
	case x < 85: // fresh-key batch plus a witness key
		g.freshBatch(op)
	default: // mixed batch (D3), including PUT X;DEL X and DEL X;PUT X (B5)
		g.mixedBatch(op)
	}
	return op
}

func (g *Generator) freshBatch(op *Op) {
	op.Kind = KindBatch
	slot := g.lane*g.p.BatchSlot + g.r.intn(g.p.BatchSlot)
	n := g.r.between(g.p.BatchMin, g.p.BatchMax)
	for i := 0; i < n; i++ {
		op.Parts = append(op.Parts, Part{Key: []byte(fmt.Sprintf("b%04d-%d", slot, i)), Size: g.valueSize(false)})
	}
	w := []byte(fmt.Sprintf("w%04d", slot))
	op.Witness = w
	op.Parts = append(op.Parts, Part{Key: w, Size: g.r.between(valueShortMin, valueHeaderLen-1)})
}

func (g *Generator) mixedBatch(op *Op) {
	op.Kind = KindBatch
	n := g.r.between(g.p.BatchMin, g.p.BatchMax-2)
	for i := 0; i < n; i++ {
		key := g.anyKey()
		if g.r.intn(10) < 7 {
			op.Parts = append(op.Parts, Part{Key: key, Size: g.valueSize(true)})
		} else {
			op.Parts = append(op.Parts, Part{Del: true, Key: key})
		}
		if g.r.intn(5) == 0 { // B5: the same key again with the opposite kind
			last := op.Parts[len(op.Parts)-1]
			if last.Del {
				op.Parts = append(op.Parts, Part{Key: last.Key, Size: g.valueSize(true)})
			} else {
				op.Parts = append(op.Parts, Part{Del: true, Key: last.Key})
			}
		}
	}
}

// PayloadBytes is 4 + sum(7 + key_len + value_len) over the decoded ops (SPEC 4.4, MASTER 8.2).
func PayloadBytes(parts []Part) int64 {
	n := int64(4)
	for _, p := range parts {
		n += 7 + int64(len(p.Key))
		if !p.Del {
			n += int64(p.Size)
		}
	}
	return n
}

var b64 = base64.StdEncoding

func encKey(k []byte) string { return b64.EncodeToString(k) }

func encValue(v []byte) string {
	if len(v) == 0 {
		return "-"
	}
	return b64.EncodeToString(v)
}

func partLine(op *Op, p Part) string {
	if p.Del {
		return "DEL " + encKey(p.Key)
	}
	return "PUT " + encKey(p.Key) + " " + encValue(EncodeValue(p.Key, op.ID, uint32(op.Lane), p.Size))
}

// EncodeRequest builds the exact bytes to send for op in b64 mode (MASTER_PROMPT 8.2). A BATCH is
// the header line followed by exactly len(Parts) op lines; the caller must send it atomically.
func EncodeRequest(op *Op) []byte {
	var sb strings.Builder
	id, lane := strconv.FormatUint(op.ID, 10), strconv.Itoa(op.Lane)
	switch op.Kind {
	case KindPut, KindDel:
		line := partLine(op, op.Parts[0])
		sb.WriteString(id + " " + lane + " " + line + "\n")
	case KindBatch:
		fmt.Fprintf(&sb, "%s %s BATCH %d %d\n", id, lane, len(op.Parts), PayloadBytes(op.Parts))
		for _, p := range op.Parts {
			sb.WriteString(partLine(op, p))
			sb.WriteByte('\n')
		}
	case KindCompact:
		sb.WriteString(id + " " + lane + " COMPACT\n")
	}
	return []byte(sb.String())
}
