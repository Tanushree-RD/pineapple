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

// Workload profiles (SPEC 15.2).
const (
	ProfileDefault      = "default"
	ProfileLargeValues  = "large-values"
	ProfileLargeBatches = "large-batches"
)

// BufferThreshold is where serve switches from buffering a BATCH to streaming it (MASTER_PROMPT 8.2).
const BufferThreshold int64 = 64 << 20

// WorkloadParams shapes a generator.
type WorkloadParams struct {
	Profile   string
	KeySpace  int // size of the shared ordinary keyspace (about 20k in default mode)
	HotKeys   int // size of the shared hot set that all lanes fight over (D2)
	MaxValue  int // upper bound of the common value-size range
	BatchMin  int
	BatchMax  int
	BatchSlot int // batch slots per lane; batch keys are recycled inside a lane-private range

	LargeMin, LargeMax int   // large-values: every PUT value is in this range
	BigOps             int   // large-batches: ops in each big batch (10,000 in the real mode)
	StreamThreshold    int64 // large-batches: payload size that separates buffered from streamed
}

// DefaultParams are the default-mode parameters.
func DefaultParams() WorkloadParams {
	return WorkloadParams{Profile: ProfileDefault, KeySpace: 20000, HotKeys: 32, MaxValue: 256, BatchMin: 2, BatchMax: 8, BatchSlot: 128}
}

// LargeValuesParams: 256 KiB to 1 MiB values (A7). The keyspace is tiny on purpose: the whole
// dump has to be read back after every kill, and 1 MiB values add up quickly.
func LargeValuesParams() WorkloadParams {
	p := DefaultParams()
	p.Profile = ProfileLargeValues
	p.KeySpace, p.HotKeys = 48, 8
	p.LargeMin, p.LargeMax = 256<<10, 1<<20
	p.BatchMin, p.BatchMax = 2, 3
	return p
}

// LargeBatchesParams: lane 0 sends 10,000-op batches, buffered (payload <= 64 MiB) most of the
// time and streamed (> 64 MiB) every 4th, while the other lanes send small concurrent writes (B6, D3).
func LargeBatchesParams() WorkloadParams {
	p := DefaultParams()
	p.Profile = ProfileLargeBatches
	p.KeySpace, p.HotKeys = 200, 16
	p.BigOps = 10000
	p.StreamThreshold = BufferThreshold
	return p
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
	op := &Op{ID: opID(g.iter, g.lane, g.seq), Lane: g.lane}
	switch g.p.Profile {
	case ProfileLargeValues:
		g.nextLargeValues(op)
	case ProfileLargeBatches:
		if g.lane == 0 {
			g.bigBatch(op)
		} else {
			g.nextDefault(op)
		}
	default:
		g.nextDefault(op)
	}
	return op
}

func (g *Generator) nextDefault(op *Op) {
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
}

func (g *Generator) largeSize() int { return g.r.between(g.p.LargeMin, g.p.LargeMax) }

func (g *Generator) nextLargeValues(op *Op) {
	switch x := g.r.intn(100); {
	case x < 60:
		op.Kind = KindPut
		op.Parts = []Part{{Key: g.normalKey(), Size: g.largeSize()}}
	case x < 75:
		op.Kind = KindPut
		op.Parts = []Part{{Key: g.hotKey(), Size: g.largeSize()}}
	case x < 85:
		op.Kind = KindDel
		op.Parts = []Part{{Del: true, Key: g.anyKey()}}
	default: // a small batch of large values plus a witness
		op.Kind = KindBatch
		n := g.r.between(g.p.BatchMin, g.p.BatchMax)
		for i := 0; i < n; i++ {
			op.Parts = append(op.Parts, Part{Key: g.normalKey(), Size: g.largeSize()})
		}
		w := []byte(fmt.Sprintf("lvw%02d", g.r.intn(g.p.HotKeys)))
		op.Witness = w
		op.Parts = append(op.Parts, Part{Key: w, Size: g.r.between(valueShortMin, valueHeaderLen-1)})
	}
}

// bigBatch builds one BigOps-op batch. Every 4th is sized above StreamThreshold so serve must take
// the ApplyStream path; the rest stay below it (buffered, Apply path). Keys are recycled over three
// slots so the database stays bounded while consecutive big batches overwrite one another.
func (g *Generator) bigBatch(op *Op) {
	op.Kind = KindBatch
	streamed := g.seq%4 == 0
	slot := int(g.seq % 3)
	valMin, valMax := 64, 400
	if streamed {
		v := int(g.p.StreamThreshold/int64(g.p.BigOps)) + 64
		valMin, valMax = v, v
	}
	for i := 0; i < g.p.BigOps; i++ {
		op.Parts = append(op.Parts, Part{Key: []byte(fmt.Sprintf("lb%d-%05d", slot, i)), Size: g.r.between(valMin, valMax)})
	}
	w := []byte(fmt.Sprintf("lbw%d", slot))
	op.Witness = w
	op.Parts = append(op.Parts, Part{Key: w, Size: g.r.between(valueShortMin, valueHeaderLen-1)})
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

// EncodeRequest builds the exact bytes to send for op in b64 mode (MASTER_PROMPT 8.2). A BATCH is
// the header line followed by exactly len(Parts) op lines; the caller must send it atomically.
func EncodeRequest(op *Op) []byte {
	var buf []byte
	id, lane := strconv.FormatUint(op.ID, 10), strconv.Itoa(op.Lane)
	switch op.Kind {
	case KindPut, KindDel:
		buf = append(buf, id+" "+lane+" "...)
		buf = appendPart(buf, op, op.Parts[0])
		buf = append(buf, '\n')
	case KindBatch:
		// About 4/3 of the decoded payload plus per-line overhead; one allocation for a 70 MiB batch.
		buf = make([]byte, 0, int(PayloadBytes(op.Parts))*4/3+len(op.Parts)*16+64)
		buf = append(buf, fmt.Sprintf("%s %s BATCH %d %d\n", id, lane, len(op.Parts), PayloadBytes(op.Parts))...)
		for _, p := range op.Parts {
			buf = appendPart(buf, op, p)
			buf = append(buf, '\n')
		}
	case KindCompact:
		buf = append(buf, id+" "+lane+" COMPACT\n"...)
	}
	return buf
}

func appendPart(buf []byte, op *Op, p Part) []byte {
	if p.Del {
		buf = append(buf, "DEL "...)
		return b64.AppendEncode(buf, p.Key)
	}
	buf = append(buf, "PUT "...)
	buf = b64.AppendEncode(buf, p.Key)
	buf = append(buf, ' ')
	if p.Size == 0 {
		return append(buf, '-')
	}
	return b64.AppendEncode(buf, EncodeValue(p.Key, op.ID, uint32(op.Lane), p.Size))
}
