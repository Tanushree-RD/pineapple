package main

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"hash/crc64"
	"testing"
)

const testEpoch = 0x1122334455667788

func newImage(frames ...[]byte) []byte {
	img := append(EncodeSuperblock(SuperSlot{Valid: true, Generation: 1, Epoch: testEpoch}), make([]byte, sbSlotSize)...)
	for _, f := range frames {
		img = append(img, f...)
	}
	return img
}

func TestMaxPayloadConstant(t *testing.T) {
	if maxPayload != 10496070004 {
		t.Fatalf("MAX_PAYLOAD = %d, SPEC 2 says 10,496,070,004", maxPayload)
	}
}

// The expected bytes below are written out by hand from the SPEC 4.2 / 4.3 / 4.4 tables, so they
// test the field offsets and sizes rather than echoing the encoder.
func TestSuperblockGoldenLayout(t *testing.T) {
	b := EncodeSuperblock(SuperSlot{Generation: 3, Epoch: 0xAABBCCDDEEFF0011, CkptOff: 9000, CkptSeq: 5, ReplayStart: 9100, MaxBytes: 4194304})
	if len(b) != 4096 {
		t.Fatalf("slot is %d bytes", len(b))
	}
	want := map[int][]byte{
		0:  []byte("LFROG-SB"),
		8:  {1, 0, 0, 0},                                     // version 1
		12: {0, 0, 0, 0},                                     // reserved
		16: {3, 0, 0, 0, 0, 0, 0, 0},                         // generation
		24: {0x11, 0x00, 0xFF, 0xEE, 0xDD, 0xCC, 0xBB, 0xAA}, // epoch, little-endian
		32: {0x28, 0x23, 0, 0, 0, 0, 0, 0},                   // ckpt_off 9000
		40: {5, 0, 0, 0, 0, 0, 0, 0},                         // ckpt_seq
		48: {0x8c, 0x23, 0, 0, 0, 0, 0, 0},                   // replay_start 9100
		56: {0, 0, 0x40, 0, 0, 0, 0, 0},                      // max_bytes 4,194,304
	}
	for off, w := range want {
		if !bytes.Equal(b[off:off+len(w)], w) {
			t.Errorf("offset %d: % x, want % x", off, b[off:off+len(w)], w)
		}
	}
	if got, want := binary.LittleEndian.Uint32(b[64:68]), crc32.Checksum(b[0:64], castagnoli); got != want {
		t.Errorf("sb_crc covers bytes 0..63: %x vs %x", got, want)
	}
	if !bytes.Equal(b[68:], make([]byte, 4096-68)) {
		t.Error("bytes 68..4095 must be zero")
	}
	if s := ParseSuperblock(b); !s.Valid || s.Generation != 3 || s.MaxBytes != 4194304 || s.CkptOff != 9000 {
		t.Errorf("round trip: %+v", s)
	}
}

func TestFrameGoldenLayout(t *testing.T) {
	// BATCH with one op: PUT "a" = "b". Payload: op_count(4) + kind(1) key_len(2) value_len(4) key(1) value(1) = 13.
	f := EncodeBatchFrame(testEpoch, 7, []FileOp{{Key: []byte("a"), Value: []byte("b")}})
	if len(f) != 56+13 {
		t.Fatalf("frame is %d bytes, SPEC 4.3 says 56+L = 69", len(f))
	}
	wantHdr := []byte{
		'L', 'F', 'R', '1', // magic
		1, 1, 0, 0, // type BATCH, version 1, reserved
		0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, // epoch
		7, 0, 0, 0, 0, 0, 0, 0, // seq
		13, 0, 0, 0, 0, 0, 0, 0, // payload_len
	}
	if !bytes.Equal(f[:32], wantHdr) {
		t.Fatalf("header bytes 0..31:\n got % x\nwant % x", f[:32], wantHdr)
	}
	if got, want := binary.LittleEndian.Uint32(f[32:36]), crc32.Checksum(f[:32], castagnoli); got != want {
		t.Errorf("header_crc covers bytes 0..31")
	}
	if !bytes.Equal(f[36:40], []byte{0, 0, 0, 0}) {
		t.Errorf("bytes 36..39 are reserved zeros")
	}
	wantPayload := []byte{1, 0, 0, 0 /* op_count */, 1 /* PUT */, 1, 0 /* key_len */, 1, 0, 0, 0 /* value_len */, 'a', 'b'}
	if !bytes.Equal(f[40:53], wantPayload) {
		t.Fatalf("payload:\n got % x\nwant % x", f[40:53], wantPayload)
	}
	if got, want := binary.LittleEndian.Uint64(f[53:61]), crc64.Checksum(f[:53], crc64Table); got != want {
		t.Errorf("frame_crc covers header and payload (bytes 0..39+L)")
	}
	if string(f[61:69]) != "LF-END!!" {
		t.Errorf("end_magic = %q", f[61:69])
	}
}

func TestReadImageBasics(t *testing.T) {
	img := newImage(
		EncodeBatchFrame(testEpoch, 1, []FileOp{{Key: []byte("a"), Value: []byte("1")}, {Key: []byte("b"), Value: []byte("2")}}),
		EncodeBatchFrame(testEpoch, 2, []FileOp{{Key: []byte("a"), Value: []byte("3")}, {Del: true, Key: []byte("b")}}),
	)
	im, err := ReadImage(img)
	if err != nil {
		t.Fatal(err)
	}
	if im.Tail != "COMPLETE" || len(im.Frames) != 2 || im.LogicalEnd != int64(len(img)) {
		t.Fatalf("tail=%s frames=%d end=%d len=%d", im.Tail, len(im.Frames), im.LogicalEnd, len(img))
	}
	if string(im.State["a"]) != "3" || len(im.State) != 1 {
		t.Fatalf("state: %v", im.State)
	}
	// SPEC 4.6: value offset = F + 40 + p + 7 + key_len. First op of frame 1: p = 4.
	if got, want := im.Frames[0].Ops[0].ValueOff, int64(8192+40+4+7+1); got != want {
		t.Fatalf("value offset %d, want %d", got, want)
	}
	if !bytes.Equal(img[im.Frames[0].Ops[0].ValueOff:][:1], []byte("1")) {
		t.Fatal("ValueOff does not point at the value bytes")
	}
}

func TestReadImageOrderB5(t *testing.T) {
	putDel := newImage(EncodeBatchFrame(testEpoch, 1, []FileOp{{Key: []byte("x"), Value: []byte("v")}, {Del: true, Key: []byte("x")}}))
	delPut := newImage(EncodeBatchFrame(testEpoch, 1, []FileOp{{Del: true, Key: []byte("x")}, {Key: []byte("x"), Value: []byte("v")}}))
	a, _ := ReadImage(putDel)
	b, _ := ReadImage(delPut)
	if len(a.State) != 0 {
		t.Fatalf("PUT X; DEL X must leave X absent: %v", a.State)
	}
	if string(b.State["x"]) != "v" {
		t.Fatalf("DEL X; PUT X must leave X present: %v", b.State)
	}
}

func TestImageEmptyValueIsNotDelete(t *testing.T) {
	im, _ := ReadImage(newImage(EncodeBatchFrame(testEpoch, 1, []FileOp{{Key: []byte("k"), Value: []byte{}}})))
	if v, ok := im.State["k"]; !ok || len(v) != 0 {
		t.Fatalf("PUT with an empty value must keep the key: %v", im.State)
	}
}

// Every possible cut of the last frame: only a cut inside end_magic keeps the frame (F4).
func TestEveryTruncationOfTheLastFrame(t *testing.T) {
	first := EncodeBatchFrame(testEpoch, 1, []FileOp{{Key: []byte("a"), Value: []byte("1")}})
	last := EncodeBatchFrame(testEpoch, 2, []FileOp{{Key: []byte("b"), Value: []byte("2")}})
	full := newImage(first, last)
	lastStart := len(full) - len(last)
	for cut := 1; cut <= len(last); cut++ { // cut = number of bytes removed from the end
		im, err := ReadImage(full[:len(full)-cut])
		if err != nil {
			t.Fatal(err)
		}
		insideSentinel := cut <= 8
		if cut == len(last) { // the whole last frame is gone: a clean file with one frame
			if im.Tail != "COMPLETE" || len(im.Frames) != 1 || im.LogicalEnd != int64(lastStart) {
				t.Fatalf("cut %d: tail=%s frames=%d end=%d", cut, im.Tail, len(im.Frames), im.LogicalEnd)
			}
			continue
		}
		if insideSentinel {
			if im.Tail != "SHORT_SENTINEL" || len(im.Frames) != 2 || string(im.State["b"]) != "2" || im.LogicalEnd != int64(len(full)) {
				t.Fatalf("cut %d (F4): tail=%s frames=%d state=%v end=%d", cut, im.Tail, len(im.Frames), im.State, im.LogicalEnd)
			}
		} else {
			if im.Tail != "TORN" || len(im.Frames) != 1 || im.LogicalEnd != int64(lastStart) {
				t.Fatalf("cut %d: tail=%s frames=%d end=%d want TORN at %d", cut, im.Tail, len(im.Frames), im.LogicalEnd, lastStart)
			}
			if _, ok := im.State["b"]; ok {
				t.Fatalf("cut %d: a torn frame must not be applied", cut)
			}
		}
	}
}

// Flip each single byte of the last frame: it must never be accepted.
func TestEveryByteFlipOfTheLastFrameIsRejected(t *testing.T) {
	first := EncodeBatchFrame(testEpoch, 1, []FileOp{{Key: []byte("a"), Value: []byte("1")}})
	last := EncodeBatchFrame(testEpoch, 2, []FileOp{{Key: []byte("bb"), Value: []byte("222")}})
	full := newImage(first, last)
	lastStart := len(full) - len(last)
	for i := lastStart; i < len(full); i++ {
		bad := append([]byte(nil), full...)
		bad[i] ^= 0x01
		im, err := ReadImage(bad)
		if err != nil {
			t.Fatal(err)
		}
		if len(im.Frames) != 1 || im.Tail != "TORN" || im.LogicalEnd != int64(lastStart) {
			t.Fatalf("flip at byte %d (frame offset %d) accepted the damaged frame: frames=%d tail=%s", i, i-lastStart, len(im.Frames), im.Tail)
		}
	}
}

func TestStaleFrameBehindTheTailIsIgnored(t *testing.T) {
	a := EncodeBatchFrame(testEpoch, 1, []FileOp{{Key: []byte("a"), Value: []byte("1")}})
	stale := EncodeBatchFrame(testEpoch, 3, []FileOp{{Key: []byte("z"), Value: []byte("9")}})   // seq gap
	other := EncodeBatchFrame(testEpoch+1, 2, []FileOp{{Key: []byte("z"), Value: []byte("9")}}) // wrong epoch
	for name, tail := range map[string][]byte{"seq gap": stale, "other epoch": other} {
		im, _ := ReadImage(newImage(a, tail))
		if len(im.Frames) != 1 || im.Tail != "TORN" {
			t.Errorf("%s: frame accepted: frames=%d tail=%s", name, len(im.Frames), im.Tail)
		}
	}
}

func TestSuperblockSelection(t *testing.T) {
	slotA := EncodeSuperblock(SuperSlot{Generation: 1, Epoch: 5})
	slotB := EncodeSuperblock(SuperSlot{Generation: 2, Epoch: 5, MaxBytes: 99})
	im, err := ReadImage(append(append([]byte(nil), slotA...), slotB...))
	if err != nil || im.Chosen != 1 || im.Super.MaxBytes != 99 {
		t.Fatalf("higher generation must win: %+v %v", im, err)
	}
	torn := append([]byte(nil), slotB...)
	torn[20] ^= 0xff // fixture torn-newest-sb flips byte 20
	im, err = ReadImage(append(append([]byte(nil), slotA...), torn...))
	if err != nil || im.Chosen != 0 || im.Super.Generation != 1 {
		t.Fatalf("a torn newest slot must fall back to the older one: %+v %v", im, err)
	}
	if _, err := ReadImage(make([]byte, 8192)); err == nil {
		t.Fatal("no valid slot must be an error, never a re-initialisation")
	}
}

func TestKeydirValidation(t *testing.T) {
	a := EncodeBatchFrame(testEpoch, 1, []FileOp{{Key: []byte("a"), Value: []byte("1")}, {Key: []byte("b"), Value: []byte("2")}})
	valOff := func(i int) uint64 { return uint64(8192 + 40 + 4 + 7 + 1 + i*(7+1+1)) }
	good := []KeydirEntry{{Key: []byte("a"), ValueOff: valOff(0), ValueLen: 1}, {Key: []byte("b"), ValueOff: valOff(1), ValueLen: 1}}
	frameOff := uint64(8192 + len(a))

	im, _ := ReadImage(newImage(a, EncodeKeydirFrame(testEpoch, 2, good)))
	if len(im.Frames) != 2 || im.Tail != "COMPLETE" {
		t.Fatalf("valid KEYDIR rejected: %d frames, %s (%s)", len(im.Frames), im.Tail, im.StopReason)
	}
	if len(im.State) != 2 {
		t.Fatalf("a KEYDIR met during a scan must be skipped, not applied: %v", im.State)
	}

	bad := map[string][]KeydirEntry{
		"unsorted":         {good[1], good[0]},
		"duplicate":        {good[0], good[0]},
		"value past frame": {{Key: []byte("a"), ValueOff: frameOff, ValueLen: 1}},
		"value in sb area": {{Key: []byte("a"), ValueOff: 100, ValueLen: 1}},
	}
	for name, e := range bad {
		im, _ := ReadImage(newImage(a, EncodeKeydirFrame(testEpoch, 2, e)))
		if len(im.Frames) != 1 || im.Tail != "TORN" {
			t.Errorf("%s: invalid KEYDIR accepted (%d frames)", name, len(im.Frames))
		}
	}
	// checkpoint_seq must equal the frame's own seq (the binding check).
	wrong := EncodeKeydirFrame(testEpoch, 2, good)
	binary.LittleEndian.PutUint64(wrong[40:48], 9)
	// re-seal the frame so only the binding is wrong, not the CRC
	body := wrong[:len(wrong)-16]
	binary.LittleEndian.PutUint64(wrong[len(body):], crc64.Checksum(body, crc64Table))
	if im, _ := ReadImage(newImage(a, wrong)); len(im.Frames) != 1 {
		t.Error("KEYDIR with checkpoint_seq != frame seq accepted")
	}
}

func TestMalformedBatchPayloadsAreRejected(t *testing.T) {
	// Build payloads by hand and wrap them in a correctly sealed frame, so only the payload is wrong.
	seal := func(payload []byte) []byte { return encodeFrame(typeBatch, testEpoch, 1, payload) }
	u32 := func(n uint32) []byte { return binary.LittleEndian.AppendUint32(nil, n) }
	u16 := func(n uint16) []byte { return binary.LittleEndian.AppendUint16(nil, n) }
	op := func(kind byte, kl uint16, vl uint32, key, val string) []byte {
		b := append([]byte{kind}, u16(kl)...)
		b = append(b, u32(vl)...)
		return append(b, key+val...)
	}
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	cases := map[string][]byte{
		"op_count 0":        u32(0),
		"op_count > 10000":  cat(u32(10001), op(1, 1, 1, "a", "b")),
		"count mismatch":    cat(u32(2), op(1, 1, 1, "a", "b")),
		"trailing byte":     cat(u32(1), op(1, 1, 1, "a", "b"), []byte{0}),
		"key_len 0":         cat(u32(1), op(1, 0, 1, "", "b")),
		"value on DELETE":   cat(u32(1), op(2, 1, 1, "a", "b")),
		"bad kind":          cat(u32(1), op(3, 1, 1, "a", "b")),
		"key_len > 1024":    cat(u32(1), op(1, 1025, 0, string(make([]byte, 1025)), "")),
		"short body":        cat(u32(1), op(1, 1, 5, "a", "b")),
		"value_len > 1 MiB": cat(u32(1), op(1, 1, 1<<20+1, "a", "")),
		"header cut":        cat(u32(1), []byte{1, 1}),
	}
	for name, p := range cases {
		im, _ := ReadImage(newImage(seal(p)))
		if len(im.Frames) != 0 {
			t.Errorf("%s: malformed payload accepted", name)
		}
	}
	if im, _ := ReadImage(newImage(seal(cat(u32(1), op(1, 1, 1, "a", "b"))))); len(im.Frames) != 1 {
		t.Error("control: the well-formed payload must be accepted")
	}
}
