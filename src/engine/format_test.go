package engine

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"math"
	"testing"
)

const testEpoch = 0x1122334455667788

// ---------- superblock ----------

func TestSuperblockGoldenBytes(t *testing.T) {
	sb := Superblock{Generation: 1, Epoch: testEpoch, CkptOff: 0x100, CkptSeq: 7, ReplayStart: 0x200, MaxBytes: 4194304}
	b := sb.Marshal()
	if len(b) != SuperblockSlotSize {
		t.Fatalf("slot length %d", len(b))
	}
	want := "4c46524f472d5342" + // magic LFROG-SB
		"01000000" + "00000000" + // version, reserved
		"0100000000000000" + // generation
		"8877665544332211" + // epoch
		"0001000000000000" + // ckpt_off
		"0700000000000000" + // ckpt_seq
		"0002000000000000" + // replay_start
		"0000400000000000" // max_bytes
	if got := hex.EncodeToString(b[:64]); got != want {
		t.Fatalf("superblock bytes 0..63\n got %s\nwant %s", got, want)
	}
	crc := crc32.Checksum(b[:64], crc32.MakeTable(crc32.Castagnoli))
	if binary.LittleEndian.Uint32(b[64:]) != crc {
		t.Fatal("sb_crc is not CRC32C of bytes 0..63")
	}
	for i := SuperblockUsed; i < len(b); i++ {
		if b[i] != 0 {
			t.Fatalf("byte %d of the slot is not zero", i)
		}
	}
	got, err := ParseSuperblock(b)
	if err != nil || got != sb {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestSuperblockEveryUsedByteIsProtected(t *testing.T) {
	good := Superblock{Generation: 3, Epoch: testEpoch, MaxBytes: 99}.Marshal()
	for i := 0; i < SuperblockUsed; i++ {
		bad := append([]byte(nil), good...)
		bad[i] ^= 0x01
		if _, err := ParseSuperblock(bad); !errors.Is(err, ErrBadSuperblock) {
			t.Fatalf("flipping byte %d still parsed (err=%v)", i, err)
		}
	}
	// Bytes past the used part are not covered and must not matter.
	tail := append([]byte(nil), good...)
	tail[SuperblockUsed+5] ^= 0xff
	if _, err := ParseSuperblock(tail); err != nil {
		t.Fatalf("byte past 68 should be ignored: %v", err)
	}
	if _, err := ParseSuperblock(good[:SuperblockUsed-1]); err == nil {
		t.Fatal("short slot parsed")
	}
	if _, err := ParseSuperblock(make([]byte, SuperblockSlotSize)); err == nil {
		t.Fatal("all-zero slot parsed")
	}
}

func TestChooseSuperblock(t *testing.T) {
	a := Superblock{Generation: 1, Epoch: 5}.Marshal()
	b := Superblock{Generation: 2, Epoch: 5}.Marshal()
	zero := make([]byte, SuperblockSlotSize)

	if i, sb, err := ChooseSuperblock(a, zero); err != nil || i != 0 || sb.Generation != 1 {
		t.Fatalf("new image: %d %+v %v", i, sb, err)
	}
	if i, sb, err := ChooseSuperblock(a, b); err != nil || i != 1 || sb.Generation != 2 {
		t.Fatalf("higher generation wins: %d %+v %v", i, sb, err)
	}
	torn := append([]byte(nil), b...)
	torn[20] ^= 0xff // SPEC 15.8 torn-newest-sb
	if i, sb, err := ChooseSuperblock(a, torn); err != nil || i != 0 || sb.Generation != 1 {
		t.Fatalf("torn newest falls back to older: %d %+v %v", i, sb, err)
	}
	if _, _, err := ChooseSuperblock(zero, zero); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("no valid slot must be ErrCorrupt, got %v", err)
	}
}

func TestSuperblockNextAndOverflow(t *testing.T) {
	s := Superblock{Generation: 4, Epoch: 9, CkptOff: 1, CkptSeq: 2, ReplayStart: 3, MaxBytes: 4}
	n, err := s.Next()
	if err != nil || n.Generation != 5 || n.Epoch != 9 || n.CkptOff != 1 || n.CkptSeq != 2 || n.ReplayStart != 3 || n.MaxBytes != 4 {
		t.Fatalf("Next: %+v %v", n, err)
	}
	if _, err := (Superblock{Generation: math.MaxUint64}).Next(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("overflow must be rejected, got %v", err)
	}
}

func TestInitialImage(t *testing.T) {
	img := InitialImage(testEpoch, 123)
	if len(img) != DataStart {
		t.Fatalf("image is %d bytes", len(img))
	}
	sb, err := ParseSuperblock(img[:SuperblockSlotSize])
	if err != nil || sb.Generation != 1 || sb.Epoch != testEpoch || sb.MaxBytes != 123 || sb.CkptOff != 0 {
		t.Fatalf("slot A: %+v %v", sb, err)
	}
	if !bytes.Equal(img[SuperblockSlotSize:], make([]byte, SuperblockSlotSize)) {
		t.Fatal("slot B must be all zeros")
	}
	e, err := NewEpoch()
	if err != nil || e == 0 {
		t.Fatalf("epoch %d %v", e, err)
	}
}

func TestLimits(t *testing.T) {
	if MaxPayload != 10_496_070_004 {
		t.Fatalf("MAX_PAYLOAD = %d, SPEC 2 says 10,496,070,004", MaxPayload)
	}
}

// ---------- frame ----------

func TestHeaderGoldenBytes(t *testing.T) {
	h := EncodeHeader(FrameHeader{Type: FrameTypeBatch, Epoch: testEpoch, Seq: 1, PayloadLen: 20})
	want := "4c465231" + "01" + "01" + "0000" + // magic LFR1, type, version, reserved
		"8877665544332211" + // epoch
		"0100000000000000" + // seq
		"1400000000000000" // payload_len
	if got := hex.EncodeToString(h[:32]); got != want {
		t.Fatalf("header bytes 0..31\n got %s\nwant %s", got, want)
	}
	if binary.LittleEndian.Uint32(h[32:]) != crc32.Checksum(h[:32], crc32.MakeTable(crc32.Castagnoli)) {
		t.Fatal("header_crc wrong")
	}
	if binary.LittleEndian.Uint32(h[36:]) != 0 {
		t.Fatal("reserved bytes 36..39 must be zero")
	}
	got, err := DecodeHeader(h[:])
	if err != nil || got.Type != 1 || got.Epoch != testEpoch || got.Seq != 1 || got.PayloadLen != 20 {
		t.Fatalf("decode: %+v %v", got, err)
	}
}

func TestHeaderMalformed(t *testing.T) {
	good := EncodeHeader(FrameHeader{Type: FrameTypeBatch, Epoch: testEpoch, Seq: 1, PayloadLen: 20})
	reCRC := func(b []byte) {
		binary.LittleEndian.PutUint32(b[32:], crc32.Checksum(b[:32], Crc32cTable))
	}
	cases := []struct {
		name  string
		mut   func(b []byte)
		check string
	}{
		{"bad magic", func(b []byte) { b[0] = 'X'; reCRC(b) }, "magic"},
		{"bad type", func(b []byte) { b[4] = 9; reCRC(b) }, "type"},
		{"bad version", func(b []byte) { b[5] = 2; reCRC(b) }, "version"},
		{"reserved 6", func(b []byte) { b[6] = 1; reCRC(b) }, "reserved"},
		{"reserved 36", func(b []byte) { b[36] = 1 }, "reserved"},
		{"flip epoch", func(b []byte) { b[10] ^= 1 }, "header_crc"},
		{"flip payload_len", func(b []byte) { b[25] ^= 1 }, "header_crc"},
		{"flip crc", func(b []byte) { b[32] ^= 1 }, "header_crc"},
	}
	for _, c := range cases {
		b := append([]byte(nil), good[:]...)
		c.mut(b)
		_, err := DecodeHeader(b)
		var fe *FrameError
		if !errors.As(err, &fe) || fe.Check != c.check {
			t.Errorf("%s: got %v, want check %q", c.name, err, c.check)
		}
	}
	if _, err := DecodeHeader(good[:39]); err == nil {
		t.Error("39-byte header accepted")
	}
}

// build encodes one frame around a raw payload (so tests can craft malformed payloads).
func buildFrame(t *testing.T, typ uint8, epoch, seq uint64, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	fw, err := NewFrameWriter(&buf, typ, epoch, seq, uint64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func scan(t *testing.T, file []byte, epoch, seq uint64) (FrameInfo, error) {
	t.Helper()
	return ScanFrame(bytes.NewReader(file), int64(len(file)), 0, epoch, seq)
}

func wantCheck(t *testing.T, name string, err error, check string) {
	t.Helper()
	var fe *FrameError
	if !errors.As(err, &fe) || fe.Check != check {
		t.Errorf("%s: got %v, want frame check %q", name, err, check)
	}
}

func TestFrameLayoutAndScan(t *testing.T) {
	payload := []byte("0123456789abcdefghij") // 20 bytes
	f := buildFrame(t, FrameTypeBatch, testEpoch, 1, payload)
	if len(f) != 56+20 {
		t.Fatalf("frame is %d bytes, want 76", len(f))
	}
	if !bytes.Equal(f[40:60], payload) {
		t.Fatal("payload not at offset 40")
	}
	if !bytes.Equal(f[68:76], []byte("LF-END!!")) {
		t.Fatalf("end_magic at 48+L wrong: %q", f[68:76])
	}
	info, err := scan(t, f, testEpoch, 1)
	if err != nil || info.End != 76 || info.ShortSentinel != 0 || info.PayloadOff() != 40 {
		t.Fatalf("scan: %+v %v", info, err)
	}
}

func TestScanFrameRejects(t *testing.T) {
	good := buildFrame(t, FrameTypeBatch, testEpoch, 5, bytes.Repeat([]byte{0xAB}, 100))
	flip := func(i int) []byte {
		b := append([]byte(nil), good...)
		b[i] ^= 0x01
		return b
	}
	_, err := scan(t, good[:39], testEpoch, 5)
	wantCheck(t, "short header", err, "truncated")
	_, err = scan(t, flip(0), testEpoch, 5)
	wantCheck(t, "magic", err, "magic")
	_, err = scan(t, flip(12), testEpoch, 5)
	wantCheck(t, "header flip is caught by header_crc", err, "header_crc")
	_, err = scan(t, good, testEpoch+1, 5)
	wantCheck(t, "wrong epoch", err, "epoch")
	_, err = scan(t, good, testEpoch, 6)
	wantCheck(t, "wrong seq", err, "seq")
	_, err = scan(t, good[:60], testEpoch, 5)
	wantCheck(t, "payload cut", err, "length")
	_, err = scan(t, good[:40+100+7], testEpoch, 5) // CRC64 incomplete
	wantCheck(t, "missing crc64", err, "length")
	_, err = scan(t, flip(40+50), testEpoch, 5)
	wantCheck(t, "payload byte flipped", err, "frame_crc")
	_, err = scan(t, flip(40+100), testEpoch, 5)
	wantCheck(t, "crc64 byte flipped", err, "frame_crc")
	_, err = scan(t, flip(len(good)-1), testEpoch, 5)
	wantCheck(t, "last sentinel byte flipped", err, "sentinel")

	// payload_len above MAX_PAYLOAD with a VALID header_crc must be refused without allocating.
	hdr := EncodeHeader(FrameHeader{Type: FrameTypeBatch, Epoch: testEpoch, Seq: 1, PayloadLen: MaxPayload + 1})
	_, err = scan(t, append(hdr[:], make([]byte, 64)...), testEpoch, 1)
	wantCheck(t, "payload_len > MAX_PAYLOAD", err, "length")
	hdr = EncodeHeader(FrameHeader{Type: FrameTypeBatch, Epoch: testEpoch, Seq: 1, PayloadLen: math.MaxUint64})
	_, err = scan(t, append(hdr[:], make([]byte, 64)...), testEpoch, 1)
	wantCheck(t, "payload_len = 2^64-1", err, "length")
	// Claims a huge but legal L with a tiny file.
	hdr = EncodeHeader(FrameHeader{Type: FrameTypeBatch, Epoch: testEpoch, Seq: 1, PayloadLen: MaxPayload})
	_, err = scan(t, hdr[:], testEpoch, 1)
	wantCheck(t, "huge L, tiny file", err, "length")
}

func TestScanFrameShortSentinel(t *testing.T) {
	good := buildFrame(t, FrameTypeBatch, testEpoch, 1, []byte("payload!"))
	for missing := 0; missing <= 8; missing++ {
		f := good[:len(good)-missing]
		info, err := scan(t, f, testEpoch, 1)
		if missing == 8 {
			// Nothing of end_magic present: still accepted, 8 bytes missing (SPEC 7.3 F4 covers 1..8).
			if err != nil || info.ShortSentinel != 8 {
				t.Fatalf("missing 8: %+v %v", info, err)
			}
			continue
		}
		if err != nil || info.ShortSentinel != missing {
			t.Fatalf("missing %d: %+v %v", missing, info, err)
		}
		if info.End != int64(len(good)) {
			t.Fatalf("End must be the full frame end %d, got %d", len(good), info.End)
		}
	}
	// A wrong byte inside the partial sentinel is rejected.
	bad := append([]byte(nil), good[:len(good)-3]...)
	bad[len(bad)-1] = 'Z'
	_, err := scan(t, bad, testEpoch, 1)
	wantCheck(t, "bad partial sentinel", err, "sentinel")
	// Short sentinel needs a valid CRC64.
	bad = append([]byte(nil), good[:len(good)-3]...)
	bad[45] ^= 1
	_, err = scan(t, bad, testEpoch, 1)
	wantCheck(t, "short sentinel with corrupt payload", err, "frame_crc")
}

func TestScanFrameAtOffset(t *testing.T) {
	prefix := make([]byte, DataStart)
	f1 := buildFrame(t, FrameTypeBatch, testEpoch, 1, []byte("aaaa"))
	f2 := buildFrame(t, FrameTypeBatch, testEpoch, 2, []byte("bbbbbbbb"))
	file := append(append(prefix, f1...), f2...)
	i1, err := ScanFrame(bytes.NewReader(file), int64(len(file)), DataStart, testEpoch, 1)
	if err != nil || i1.End != int64(DataStart+len(f1)) {
		t.Fatalf("frame 1: %+v %v", i1, err)
	}
	i2, err := ScanFrame(bytes.NewReader(file), int64(len(file)), i1.End, testEpoch, 2)
	if err != nil || i2.End != int64(len(file)) {
		t.Fatalf("frame 2: %+v %v", i2, err)
	}
}

func TestFrameWriterLengthDiscipline(t *testing.T) {
	var buf bytes.Buffer
	fw, _ := NewFrameWriter(&buf, FrameTypeBatch, 1, 1, 4)
	if _, err := fw.Write([]byte("12345")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overlong write: %v", err)
	}
	fw, _ = NewFrameWriter(&buf, FrameTypeBatch, 1, 1, 4)
	fw.Write([]byte("12"))
	if err := fw.Close(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short close: %v", err)
	}
	if _, err := NewFrameWriter(&buf, FrameTypeBatch, 1, 1, MaxPayload+1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("L above MAX_PAYLOAD: %v", err)
	}
	if _, err := NewFrameWriter(&buf, 9, 1, 1, 4); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad type: %v", err)
	}
}

// maxWriteRecorder proves SPEC 4.7: no single write is bigger than 4 MiB.
type maxWriteRecorder struct {
	buf bytes.Buffer
	max int
}

func (m *maxWriteRecorder) Write(p []byte) (int, error) {
	if len(p) > m.max {
		m.max = len(p)
	}
	return m.buf.Write(p)
}

func TestStreamingNeverWritesMoreThanFourMiB(t *testing.T) {
	var ops []Op
	for i := 0; i < 12; i++ { // 12 MiB of values
		ops = append(ops, Op{Kind: OpPut, Key: []byte{byte('a' + i)}, Value: bytes.Repeat([]byte{byte(i)}, MaxValueLen)})
	}
	rec := &maxWriteRecorder{}
	if err := WriteBatchFrame(rec, testEpoch, 1, ops); err != nil {
		t.Fatal(err)
	}
	if rec.max > WriteChunk {
		t.Fatalf("largest single write %d > %d", rec.max, WriteChunk)
	}
	info, err := scan(t, rec.buf.Bytes(), testEpoch, 1)
	if err != nil {
		t.Fatal(err)
	}
	ents, err := ParseBatch(bytes.NewReader(rec.buf.Bytes()[40:]), info.Header.PayloadLen)
	if err != nil || len(ents) != 12 {
		t.Fatalf("parse: %d entries, %v", len(ents), err)
	}
}
