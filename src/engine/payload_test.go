package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func opBytes(kind byte, keyLen uint16, valLen uint32, key, val []byte) []byte {
	b := make([]byte, 7)
	b[0] = kind
	binary.LittleEndian.PutUint16(b[1:], keyLen)
	binary.LittleEndian.PutUint32(b[3:], valLen)
	return append(append(b, key...), val...)
}

func payloadOf(count uint32, ops ...[]byte) []byte {
	p := make([]byte, 4)
	binary.LittleEndian.PutUint32(p, count)
	for _, o := range ops {
		p = append(p, o...)
	}
	return p
}

func parseRaw(p []byte) ([]BatchEntry, error) {
	return ParseBatch(bytes.NewReader(p), uint64(len(p)))
}

func TestBatchRoundTripAndValueOffsets(t *testing.T) {
	ops := []Op{
		{Kind: OpPut, Key: []byte("alpha"), Value: []byte("one")},
		{Kind: OpPut, Key: []byte("empty"), Value: nil}, // empty value is NOT a delete
		{Kind: OpDelete, Key: []byte("gone")},
		{Kind: OpPut, Key: []byte{0x00, '\n', 0xff}, Value: []byte{0, 1, 2, '\t'}},
	}
	size, err := BatchPayloadSize(ops)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteBatchFrame(&buf, testEpoch, 1, ops); err != nil {
		t.Fatal(err)
	}
	file := buf.Bytes()
	if int64(len(file)) != 56+size {
		t.Fatalf("frame %d bytes, want %d", len(file), 56+size)
	}
	// 4 + (7+5+3) + (7+5+0) + (7+4+0) + (7+3+4) = 56
	if size != 56 {
		t.Fatalf("payload size %d, want 56", size)
	}
	info, err := scan(t, file, testEpoch, 1)
	if err != nil {
		t.Fatal(err)
	}
	ents, err := ParseBatch(bytes.NewReader(file[info.PayloadOff():]), info.Header.PayloadLen)
	if err != nil || len(ents) != len(ops) {
		t.Fatalf("parse: %v %d", err, len(ents))
	}
	for i, e := range ents {
		if e.Kind != ops[i].Kind || !bytes.Equal(e.Key, ops[i].Key) || int(e.ValueLen) != len(ops[i].Value) {
			t.Fatalf("entry %d: %+v", i, e)
		}
		// SPEC 4.6: value offset = F + 40 + p + 7 + key_len, F=0 here.
		abs := info.Off + FrameHeaderLen + int64(e.ValueOff)
		if got := file[abs : abs+int64(e.ValueLen)]; !bytes.Equal(got, ops[i].Value) {
			t.Fatalf("entry %d: value at offset %d is %q, want %q", i, abs, got, ops[i].Value)
		}
	}
	if ents[0].ValueOff != 4+7+5 {
		t.Fatalf("first value payload offset %d, want 16", ents[0].ValueOff)
	}
}

func TestBatchWriterEnforcesAnnouncements(t *testing.T) {
	good := []Op{{Kind: OpPut, Key: []byte("k"), Value: []byte("v")}}
	size, _ := BatchPayloadSize(good)
	var buf bytes.Buffer

	bw, _ := NewBatchWriter(&buf, 1, 1, 2, size+8)
	bw.Add(good[0])
	if err := bw.Close(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("fewer ops than announced: %v", err)
	}
	bw, _ = NewBatchWriter(&buf, 1, 1, 1, size)
	bw.Add(good[0])
	if err := bw.Add(good[0]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("more ops than announced: %v", err)
	}
	bw, _ = NewBatchWriter(&buf, 1, 1, 1, size+1) // size mismatch caught at Close
	bw.Add(good[0])
	if err := bw.Close(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("payload size mismatch: %v", err)
	}
	if _, err := NewBatchWriter(&buf, 1, 1, 0, 4); !errors.Is(err, ErrInvalid) {
		t.Fatalf("0 ops: %v", err)
	}
	if _, err := NewBatchWriter(&buf, 1, 1, MaxOpsPerBatch+1, MaxPayload); !errors.Is(err, ErrInvalid) {
		t.Fatalf("10001 ops: %v", err)
	}
}

func TestCheckOpLimits(t *testing.T) {
	bad := []Op{
		{Kind: OpPut, Key: nil, Value: nil},
		{Kind: OpPut, Key: make([]byte, MaxKeyLen+1)},
		{Kind: OpPut, Key: []byte("k"), Value: make([]byte, MaxValueLen+1)},
		{Kind: OpDelete, Key: []byte("k"), Value: []byte("v")},
		{Kind: 0, Key: []byte("k")},
		{Kind: 3, Key: []byte("k")},
	}
	for i, op := range bad {
		if _, err := BatchPayloadSize([]Op{op}); !errors.Is(err, ErrInvalid) {
			t.Errorf("op %d accepted", i)
		}
	}
	for _, op := range []Op{
		{Kind: OpPut, Key: make([]byte, MaxKeyLen), Value: make([]byte, MaxValueLen)},
		{Kind: OpPut, Key: []byte("k")},
	} {
		if _, err := BatchPayloadSize([]Op{op}); err != nil {
			t.Errorf("limit op rejected: %v", err)
		}
	}
	if _, err := BatchPayloadSize(nil); !errors.Is(err, ErrInvalid) {
		t.Error("empty batch accepted")
	}
	tooMany := make([]Op, MaxOpsPerBatch+1)
	for i := range tooMany {
		tooMany[i] = Op{Kind: OpPut, Key: []byte("k")}
	}
	if _, err := BatchPayloadSize(tooMany); !errors.Is(err, ErrInvalid) {
		t.Error("10001 ops accepted")
	}
	if _, err := BatchPayloadSize(tooMany[:MaxOpsPerBatch]); err != nil {
		t.Errorf("10000 ops rejected: %v", err)
	}
}

func TestParseBatchMalformed(t *testing.T) {
	ok := opBytes(1, 1, 1, []byte("k"), []byte("v"))
	del := opBytes(2, 1, 0, []byte("k"), nil)
	cases := []struct {
		name string
		p    []byte
	}{
		{"empty payload", nil},
		{"op_count 0", payloadOf(0)},
		{"op_count 10001", payloadOf(10001, ok)},
		{"op_count larger than ops present", payloadOf(2, ok)},
		{"op_count smaller than ops present", payloadOf(1, ok, ok)},
		{"trailing byte", append(payloadOf(1, ok), 0)},
		{"huge op_count in tiny payload", payloadOf(10000, ok)},
		{"key_len 0", payloadOf(1, opBytes(1, 0, 0, nil, nil))},
		{"key_len 1025", payloadOf(1, opBytes(1, 1025, 0, make([]byte, 1025), nil))},
		{"value_len above 1 MiB", payloadOf(1, opBytes(1, 1, MaxValueLen+1, []byte("k"), nil))},
		{"value on DELETE", payloadOf(1, opBytes(2, 1, 1, []byte("k"), []byte("v")))},
		{"kind 0", payloadOf(1, opBytes(0, 1, 0, []byte("k"), nil))},
		{"kind 3", payloadOf(1, opBytes(3, 1, 0, []byte("k"), nil))},
		{"header cut", payloadOf(1, ok[:5])},
		{"key cut", payloadOf(1, opBytes(1, 4, 0, []byte("ab"), nil))},
		{"value cut", payloadOf(1, opBytes(1, 1, 10, []byte("k"), []byte("abc")))},
		{"delete then trailing garbage", append(payloadOf(1, del), 1, 2, 3)},
	}
	for _, c := range cases {
		_, err := parseRaw(c.p)
		var fe *FrameError
		if !errors.As(err, &fe) || fe.Check != "payload" {
			t.Errorf("%s: got %v, want payload error", c.name, err)
		}
	}
	// Sanity: the well-formed ones around them parse.
	if _, err := parseRaw(payloadOf(2, ok, del)); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	// 10,000 ops is the maximum and must parse.
	max := make([][]byte, MaxOpsPerBatch)
	for i := range max {
		max[i] = ok
	}
	if ents, err := parseRaw(payloadOf(MaxOpsPerBatch, max...)); err != nil || len(ents) != MaxOpsPerBatch {
		t.Fatalf("10000 ops: %v", err)
	}
}

func TestBatchPayloadErrorFromAFramedBadPayloadIsStillAFrameCRCMatch(t *testing.T) {
	// A payload that is structurally bad but has a valid CRC64 passes ScanFrame (checks 1-7)
	// and is rejected only by the payload parse (check 8). Recovery must run both.
	bad := payloadOf(2, opBytes(1, 1, 1, []byte("k"), []byte("v")))
	f := buildFrame(t, FrameTypeBatch, testEpoch, 1, bad)
	info, err := scan(t, f, testEpoch, 1)
	if err != nil {
		t.Fatalf("ScanFrame should accept a CRC-valid frame: %v", err)
	}
	if _, err := ParseBatch(bytes.NewReader(f[40:]), info.Header.PayloadLen); err == nil {
		t.Fatal("ParseBatch must reject it")
	}
}

// ---------- KEYDIR ----------

func keydirEntries() []KeydirEntry {
	return []KeydirEntry{
		{Key: []byte("a"), ValueOff: DataStart + 10, ValueLen: 5},
		{Key: []byte("b"), ValueOff: DataStart + 20, ValueLen: 0},
		{Key: []byte("cc"), ValueOff: DataStart + 30, ValueLen: 100},
	}
}

func rawKeydir(seq, count uint64, entries ...[]byte) []byte {
	p := make([]byte, 16)
	binary.LittleEndian.PutUint64(p[0:], seq)
	binary.LittleEndian.PutUint64(p[8:], count)
	for _, e := range entries {
		p = append(p, e...)
	}
	return p
}

func rawEntry(key string, off uint64, vlen uint32) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(len(key)))
	b = append(b, key...)
	t := make([]byte, 12)
	binary.LittleEndian.PutUint64(t[0:], off)
	binary.LittleEndian.PutUint32(t[8:], vlen)
	return append(b, t...)
}

func parseKD(p []byte, seq, frameOff uint64) ([]KeydirEntry, error) {
	return ParseKeydir(bytes.NewReader(p), uint64(len(p)), seq, frameOff)
}

func TestKeydirRoundTrip(t *testing.T) {
	ents := keydirEntries()
	const frameOff = DataStart + 1000
	var buf bytes.Buffer
	if err := WriteKeydirFrame(&buf, testEpoch, 42, ents); err != nil {
		t.Fatal(err)
	}
	file := buf.Bytes()
	// 16 + (14+1) + (14+1) + (14+2) = 62
	if KeydirPayloadSize(ents) != 62 || len(file) != 56+62 {
		t.Fatalf("size %d / frame %d", KeydirPayloadSize(ents), len(file))
	}
	info, err := scan(t, file, testEpoch, 42)
	if err != nil || info.Header.Type != FrameTypeKeydir {
		t.Fatalf("scan: %+v %v", info, err)
	}
	if got := binary.LittleEndian.Uint64(file[40:]); got != 42 {
		t.Fatalf("payload checkpoint_seq = %d, want the frame's own seq 42", got)
	}
	got, err := ParseKeydir(bytes.NewReader(file[40:]), info.Header.PayloadLen, 42, frameOff)
	if err != nil || len(got) != 3 {
		t.Fatalf("parse: %v", err)
	}
	for i := range got {
		if !bytes.Equal(got[i].Key, ents[i].Key) || got[i].ValueOff != ents[i].ValueOff || got[i].ValueLen != ents[i].ValueLen {
			t.Fatalf("entry %d mismatch: %+v", i, got[i])
		}
	}
	// An empty KEYDIR (a compacted empty database): 8192 + 72 bytes is the SPEC 10 result.
	buf.Reset()
	if err := WriteKeydirFrame(&buf, testEpoch, 1, nil); err != nil || buf.Len() != 72 {
		t.Fatalf("empty keydir frame is %d bytes (%v), want 72", buf.Len(), err)
	}
}

func TestKeydirWriterRejectsBadInput(t *testing.T) {
	var buf bytes.Buffer
	bad := [][]KeydirEntry{
		{{Key: []byte("b")}, {Key: []byte("a")}},
		{{Key: []byte("a")}, {Key: []byte("a")}},
		{{Key: nil}},
		{{Key: make([]byte, MaxKeyLen+1)}},
		{{Key: []byte("a"), ValueLen: MaxValueLen + 1}},
	}
	for i, e := range bad {
		if err := WriteKeydirFrame(&buf, testEpoch, 1, e); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d accepted: %v", i, err)
		}
	}
}

func TestParseKeydirMalformed(t *testing.T) {
	const frameOff = DataStart + 1000
	a := rawEntry("a", DataStart+10, 5)
	b := rawEntry("b", DataStart+20, 5)
	cases := []struct {
		name string
		p    []byte
	}{
		{"checkpoint_seq != frame seq", rawKeydir(8, 1, a)},
		{"unsorted", rawKeydir(7, 2, b, a)},
		{"duplicate key", rawKeydir(7, 2, a, a)},
		{"key_len 0", rawKeydir(7, 1, rawEntry("", DataStart, 0))},
		{"value_off below 8192", rawKeydir(7, 1, rawEntry("a", 100, 5))},
		{"value runs past the keydir frame", rawKeydir(7, 1, rawEntry("a", frameOff-2, 5))},
		{"value_off beyond the frame", rawKeydir(7, 1, rawEntry("a", frameOff+1, 0))},
		{"value_len above 1 MiB", rawKeydir(7, 1, rawEntry("a", DataStart, MaxValueLen+1))},
		{"entry_count too big for payload", rawKeydir(7, 1<<40, a)},
		{"entry_count larger than entries", rawKeydir(7, 2, a)},
		{"entry_count smaller than entries", rawKeydir(7, 1, a, b)},
		{"trailing byte", append(rawKeydir(7, 1, a), 9)},
		{"header only half", make([]byte, 8)},
		{"entry cut", rawKeydir(7, 1, a[:5])},
	}
	for _, c := range cases {
		_, err := parseKD(c.p, 7, frameOff)
		var fe *FrameError
		if !errors.As(err, &fe) || fe.Check != "payload" {
			t.Errorf("%s: got %v, want payload error", c.name, err)
		}
	}
	if _, err := parseKD(rawKeydir(7, 2, a, b), 7, frameOff); err != nil {
		t.Fatalf("valid keydir rejected: %v", err)
	}
	// A value ending exactly at the keydir frame's offset is the largest legal one.
	if _, err := parseKD(rawKeydir(7, 1, rawEntry("a", frameOff-5, 5)), 7, frameOff); err != nil {
		t.Fatalf("value ending at frame start rejected: %v", err)
	}
}
