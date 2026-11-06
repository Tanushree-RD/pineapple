package main

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func genOps(seed uint64, iter, lane, n int) []*Op {
	g := NewGenerator(DefaultParams(), seed, iter, lane)
	out := make([]*Op, n)
	for i := range out {
		out[i] = g.Next()
	}
	return out
}

func TestGeneratorDeterministic(t *testing.T) {
	a := genOps(12345, 3, 1, 500)
	b := genOps(12345, 3, 1, 500)
	for i := range a {
		if !bytes.Equal(EncodeRequest(a[i]), EncodeRequest(b[i])) {
			t.Fatalf("op %d differs between identical generators", i)
		}
	}
	c := genOps(12346, 3, 1, 500)
	same := 0
	for i := range a {
		if bytes.Equal(EncodeRequest(a[i]), EncodeRequest(c[i])) {
			same++
		}
	}
	if same > 5 {
		t.Fatalf("different seeds produced %d identical ops of 500", same)
	}
}

func TestOpIDsUniqueAcrossIterationsAndLanes(t *testing.T) {
	seen := map[uint64]bool{}
	for iter := 1; iter <= 20; iter++ {
		for lane := 0; lane < 4; lane++ {
			for _, op := range genOps(7, iter, lane, 200) {
				if op.ID == 0 || seen[op.ID] {
					t.Fatalf("duplicate or zero op id %d", op.ID)
				}
				seen[op.ID] = true
			}
		}
	}
}

func TestGeneratedOpsAreLegal(t *testing.T) {
	var kinds = map[OpKind]int{}
	var b5put, b5del, empty, short, witness int
	for lane := 0; lane < 4; lane++ {
		for _, op := range genOps(99, 1, lane, 4000) {
			kinds[op.Kind]++
			if len(op.Parts) == 0 {
				t.Fatalf("op %d has no parts", op.ID)
			}
			if op.Kind == KindBatch && (len(op.Parts) < 2 || len(op.Parts) > 12) {
				t.Fatalf("batch op %d has %d parts", op.ID, len(op.Parts))
			}
			if op.Kind != KindBatch && len(op.Parts) != 1 {
				t.Fatalf("single op %d has %d parts", op.ID, len(op.Parts))
			}
			if len(op.Witness) > 0 {
				witness++
				last := op.Parts[len(op.Parts)-1]
				if !bytes.Equal(last.Key, op.Witness) || last.Del || last.Size < valueShortMin {
					t.Fatalf("witness must be the last part and a real PUT: %+v", op)
				}
			}
			for i, p := range op.Parts {
				if len(p.Key) < 1 || len(p.Key) > 1024 {
					t.Fatalf("bad key length %d", len(p.Key))
				}
				if !p.Del {
					if p.Size == 0 {
						empty++
					} else if p.Size < valueShortMin || p.Size > valueMaxLen {
						t.Fatalf("illegal value size %d", p.Size)
					} else if p.Size < valueHeaderLen {
						short++
					}
				}
				if i > 0 && bytes.Equal(op.Parts[i-1].Key, p.Key) {
					if op.Parts[i-1].Del && !p.Del {
						b5put++ // DEL X; PUT X
					}
					if !op.Parts[i-1].Del && p.Del {
						b5del++ // PUT X; DEL X
					}
				}
			}
		}
	}
	for _, k := range []OpKind{KindPut, KindDel, KindBatch} {
		if kinds[k] == 0 {
			t.Errorf("never generated a %v", k)
		}
	}
	if b5put == 0 || b5del == 0 || empty == 0 || short == 0 || witness == 0 {
		t.Errorf("coverage gap: DEL;PUT=%d PUT;DEL=%d empty=%d short=%d witness=%d", b5put, b5del, empty, short, witness)
	}
}

func TestEncodeRequestFormat(t *testing.T) {
	put := &Op{ID: 7, Lane: 2, Kind: KindPut, Parts: []Part{{Key: []byte("a"), Size: 12}}}
	got := string(EncodeRequest(put))
	f := strings.Fields(got)
	if len(f) != 5 || f[0] != "7" || f[1] != "2" || f[2] != "PUT" || f[3] != "YQ==" || !strings.HasSuffix(got, "\n") {
		t.Fatalf("PUT line: %q", got)
	}
	v, err := b64.DecodeString(f[4])
	if err != nil || len(v) != 12 {
		t.Fatalf("PUT value: %v len=%d", err, len(v))
	}

	empty := &Op{ID: 8, Lane: 0, Kind: KindPut, Parts: []Part{{Key: []byte("a"), Size: 0}}}
	if got := string(EncodeRequest(empty)); got != "8 0 PUT YQ== -\n" {
		t.Fatalf("empty value must be '-': %q", got)
	}
	del := &Op{ID: 9, Lane: 1, Kind: KindDel, Parts: []Part{{Del: true, Key: []byte("a")}}}
	if got := string(EncodeRequest(del)); got != "9 1 DEL YQ==\n" {
		t.Fatalf("DEL line: %q", got)
	}
	if got := string(EncodeRequest(&Op{ID: 10, Lane: 3, Kind: KindCompact})); got != "10 3 COMPACT\n" {
		t.Fatalf("COMPACT line: %q", got)
	}
}

func TestEncodeBatchPayloadBytesAreExact(t *testing.T) {
	for _, op := range genOps(5, 2, 0, 3000) {
		if op.Kind != KindBatch {
			continue
		}
		req := string(EncodeRequest(op))
		lines := strings.Split(strings.TrimSuffix(req, "\n"), "\n")
		hdr := strings.Fields(lines[0])
		if hdr[2] != "BATCH" || hdr[3] != strconv.Itoa(len(op.Parts)) || len(lines) != 1+len(op.Parts) {
			t.Fatalf("batch framing wrong: %q", lines[0])
		}
		// Recompute the decoded payload size from the lines themselves.
		var want int64 = 4
		for _, ln := range lines[1:] {
			f := strings.Fields(ln)
			k, err := b64.DecodeString(f[1])
			if err != nil {
				t.Fatal(err)
			}
			want += 7 + int64(len(k))
			if f[0] == "PUT" && f[2] != "-" {
				v, err := b64.DecodeString(f[2])
				if err != nil {
					t.Fatal(err)
				}
				want += int64(len(v))
			}
		}
		if hdr[4] != strconv.FormatInt(want, 10) {
			t.Fatalf("payload_bytes %s but decoded ops total %d", hdr[4], want)
		}
		return
	}
	t.Fatal("no batch generated")
}

func TestKillDelayRange(t *testing.T) {
	lo, hi := 1000, 0
	for i := 0; i < 5000; i++ {
		d := killDelayMS(iterSeed(1, i))
		if d < lo {
			lo = d
		}
		if d > hi {
			hi = d
		}
	}
	if lo < 10 || hi > 250 || lo > 15 || hi < 245 {
		t.Fatalf("kill delay range [%d,%d], want about [10,250]", lo, hi)
	}
}

func TestSeedDerivationIndependent(t *testing.T) {
	if iterSeed(1, 1) == iterSeed(1, 2) || laneSeed(5, 0) == laneSeed(5, 1) {
		t.Fatal("seeds must differ per iteration and lane")
	}
}
