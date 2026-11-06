package engine

// BATCH (type 1) and KEYDIR (type 2) payloads (SPEC 4.4, 4.5): streaming encoders and
// exact parsers. Parsers implement SPEC 7.2 check 8: every field in bounds and zero bytes
// left over, otherwise the whole frame is invalid and none of its ops is applied.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

func parseBufSize(L uint64) int {
	if L > 64<<10 {
		return 64 << 10
	}
	if L < 16 {
		return 16
	}
	return int(L)
}

// ---------- BATCH ----------

// BatchPayloadSize is 4 + sum(7 + key_len + value_len), after validating every op.
func BatchPayloadSize(ops []Op) (int64, error) {
	if len(ops) < 1 || len(ops) > MaxOpsPerBatch {
		return 0, fmt.Errorf("%w: %d ops, want 1..%d", ErrInvalid, len(ops), MaxOpsPerBatch)
	}
	size := int64(4)
	for _, op := range ops {
		if err := checkOp(op); err != nil {
			return 0, err
		}
		size += opSize(op)
	}
	return size, nil
}

// BatchWriter streams one BATCH frame op by op. The caller announces the op count and the
// exact payload size up front; Close fails if either does not match.
type BatchWriter struct {
	fw   *FrameWriter
	left int
	err  error
}

func NewBatchWriter(w io.Writer, epoch, seq uint64, count int, payloadBytes int64) (*BatchWriter, error) {
	if count < 1 || count > MaxOpsPerBatch {
		return nil, fmt.Errorf("%w: %d ops, want 1..%d", ErrInvalid, count, MaxOpsPerBatch)
	}
	if payloadBytes < 4+int64(count)*(batchOpHeaderLen+1) || payloadBytes > MaxPayload {
		return nil, fmt.Errorf("%w: payload size %d impossible for %d ops", ErrInvalid, payloadBytes, count)
	}
	fw, err := NewFrameWriter(w, FrameTypeBatch, epoch, seq, uint64(payloadBytes))
	if err != nil {
		return nil, err
	}
	b := &BatchWriter{fw: fw, left: count}
	var c [4]byte
	binary.LittleEndian.PutUint32(c[:], uint32(count))
	if _, err := fw.Write(c[:]); err != nil {
		return nil, err
	}
	return b, nil
}

// Add appends one op. The op's value bytes are written straight through; nothing is copied.
func (b *BatchWriter) Add(op Op) error {
	if b.err != nil {
		return b.err
	}
	if b.left == 0 {
		b.err = fmt.Errorf("%w: more ops than announced", ErrInvalid)
		return b.err
	}
	if err := checkOp(op); err != nil {
		b.err = err
		return err
	}
	var h [batchOpHeaderLen]byte
	h[0] = byte(op.Kind)
	binary.LittleEndian.PutUint16(h[1:], uint16(len(op.Key)))
	binary.LittleEndian.PutUint32(h[3:], uint32(len(op.Value)))
	for _, p := range [][]byte{h[:], op.Key, op.Value} {
		if len(p) == 0 {
			continue
		}
		if _, err := b.fw.Write(p); err != nil {
			b.err = err
			return err
		}
	}
	b.left--
	return nil
}

// Close finishes the frame. It fails if fewer ops than announced were added.
func (b *BatchWriter) Close() error {
	if b.err != nil {
		return b.err
	}
	if b.left != 0 {
		b.err = fmt.Errorf("%w: %d ops announced but not added", ErrInvalid, b.left)
		return b.err
	}
	return b.fw.Close()
}

// WriteBatchFrame encodes ops as one frame (the buffered path).
func WriteBatchFrame(w io.Writer, epoch, seq uint64, ops []Op) error {
	size, err := BatchPayloadSize(ops)
	if err != nil {
		return err
	}
	bw, err := NewBatchWriter(w, epoch, seq, len(ops), size)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err := bw.Add(op); err != nil {
			return err
		}
	}
	return bw.Close()
}

// BatchEntry is one parsed op. ValueOff is relative to the payload start, so the absolute
// offset of the value is frameOff + 40 + ValueOff (SPEC 4.6).
type BatchEntry struct {
	Kind     OpKind
	Key      []byte
	ValueLen uint32
	ValueOff uint64
}

// ParseBatch reads a BATCH payload of exactly L bytes from r, skipping value bytes.
func ParseBatch(r io.Reader, L uint64) ([]BatchEntry, error) {
	br := bufio.NewReaderSize(io.LimitReader(r, int64(L)), parseBufSize(L))
	var pos uint64
	var cb [4]byte
	if _, err := io.ReadFull(br, cb[:]); err != nil {
		return nil, frameErr("payload", "no op_count")
	}
	pos = 4
	count := binary.LittleEndian.Uint32(cb[:])
	if count < 1 || count > MaxOpsPerBatch {
		return nil, frameErr("payload", "op_count %d not in 1..%d", count, MaxOpsPerBatch)
	}
	if uint64(count)*(batchOpHeaderLen+1)+4 > L {
		return nil, frameErr("payload", "op_count %d cannot fit in %d bytes", count, L)
	}
	entries := make([]BatchEntry, 0, count)
	for i := uint32(0); i < count; i++ {
		var h [batchOpHeaderLen]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			return nil, frameErr("payload", "op %d: header runs past payload", i)
		}
		kind := OpKind(h[0])
		keyLen := uint64(binary.LittleEndian.Uint16(h[1:]))
		valLen := uint64(binary.LittleEndian.Uint32(h[3:]))
		if kind != OpPut && kind != OpDelete {
			return nil, frameErr("payload", "op %d: kind %d", i, h[0])
		}
		if keyLen < 1 || keyLen > MaxKeyLen {
			return nil, frameErr("payload", "op %d: key_len %d", i, keyLen)
		}
		if valLen > MaxValueLen {
			return nil, frameErr("payload", "op %d: value_len %d", i, valLen)
		}
		if kind == OpDelete && valLen != 0 {
			return nil, frameErr("payload", "op %d: delete with value_len %d", i, valLen)
		}
		if pos+batchOpHeaderLen+keyLen+valLen > L {
			return nil, frameErr("payload", "op %d: runs past payload end", i)
		}
		key := make([]byte, keyLen)
		if _, err := io.ReadFull(br, key); err != nil {
			return nil, frameErr("payload", "op %d: key truncated", i)
		}
		valOff := pos + batchOpHeaderLen + keyLen
		if _, err := br.Discard(int(valLen)); err != nil {
			return nil, frameErr("payload", "op %d: value truncated", i)
		}
		pos = valOff + valLen
		entries = append(entries, BatchEntry{Kind: kind, Key: key, ValueLen: uint32(valLen), ValueOff: valOff})
	}
	if pos != L {
		return nil, frameErr("payload", "%d trailing bytes after %d ops", L-pos, count)
	}
	return entries, nil
}

// ---------- KEYDIR ----------

// KeydirEntry maps a key to the ABSOLUTE file offset of its value bytes.
type KeydirEntry struct {
	Key      []byte
	ValueOff uint64
	ValueLen uint32
}

// KeydirPayloadSize is 16 + sum(14 + key_len).
func KeydirPayloadSize(entries []KeydirEntry) int64 {
	size := int64(keydirHeaderLen)
	for _, e := range entries {
		size += 14 + int64(len(e.Key))
	}
	return size
}

// WriteKeydirFrame encodes entries (keys strictly ascending) as one KEYDIR frame whose
// payload checkpoint_seq equals the frame's own seq (SPEC 4.5 binding check).
func WriteKeydirFrame(w io.Writer, epoch, seq uint64, entries []KeydirEntry) error {
	for i, e := range entries {
		if len(e.Key) < 1 || len(e.Key) > MaxKeyLen {
			return fmt.Errorf("%w: keydir key %d length %d", ErrInvalid, i, len(e.Key))
		}
		if e.ValueLen > MaxValueLen {
			return fmt.Errorf("%w: keydir value %d length %d", ErrInvalid, i, e.ValueLen)
		}
		if i > 0 && bytes.Compare(entries[i-1].Key, e.Key) >= 0 {
			return fmt.Errorf("%w: keydir keys not strictly ascending at %d", ErrInvalid, i)
		}
	}
	size := KeydirPayloadSize(entries)
	if size > MaxPayload {
		return fmt.Errorf("%w: keydir payload %d too large", ErrInvalid, size)
	}
	fw, err := NewFrameWriter(w, FrameTypeKeydir, epoch, seq, uint64(size))
	if err != nil {
		return err
	}
	var h [keydirHeaderLen]byte
	binary.LittleEndian.PutUint64(h[0:], seq)
	binary.LittleEndian.PutUint64(h[8:], uint64(len(entries)))
	if _, err := fw.Write(h[:]); err != nil {
		return err
	}
	for _, e := range entries {
		var kl [2]byte
		var tail [12]byte
		binary.LittleEndian.PutUint16(kl[:], uint16(len(e.Key)))
		binary.LittleEndian.PutUint64(tail[0:], e.ValueOff)
		binary.LittleEndian.PutUint32(tail[8:], e.ValueLen)
		for _, p := range [][]byte{kl[:], e.Key, tail[:]} {
			if _, err := fw.Write(p); err != nil {
				return err
			}
		}
	}
	return fw.Close()
}

// ParseKeydir reads a KEYDIR payload of exactly L bytes. frameSeq is the frame's own seq
// (checkpoint_seq must equal it) and frameOff is the frame's offset (every value must end
// at or before it, and start at or after DataStart).
func ParseKeydir(r io.Reader, L, frameSeq, frameOff uint64) ([]KeydirEntry, error) {
	if L < keydirHeaderLen {
		return nil, frameErr("payload", "keydir payload %d shorter than its header", L)
	}
	br := bufio.NewReaderSize(io.LimitReader(r, int64(L)), parseBufSize(L))
	var h [keydirHeaderLen]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return nil, frameErr("payload", "keydir header truncated")
	}
	if cs := binary.LittleEndian.Uint64(h[0:]); cs != frameSeq {
		return nil, frameErr("payload", "checkpoint_seq %d != frame seq %d", cs, frameSeq)
	}
	count := binary.LittleEndian.Uint64(h[8:])
	if count > (L-keydirHeaderLen)/keydirMinEntry {
		return nil, frameErr("payload", "entry_count %d cannot fit in %d bytes", count, L)
	}
	entries := make([]KeydirEntry, 0, count)
	pos := uint64(keydirHeaderLen)
	var prev []byte
	for i := uint64(0); i < count; i++ {
		var kl [2]byte
		if _, err := io.ReadFull(br, kl[:]); err != nil {
			return nil, frameErr("payload", "entry %d: key_len truncated", i)
		}
		keyLen := uint64(binary.LittleEndian.Uint16(kl[:]))
		if keyLen < 1 || keyLen > MaxKeyLen {
			return nil, frameErr("payload", "entry %d: key_len %d", i, keyLen)
		}
		if pos+14+keyLen > L {
			return nil, frameErr("payload", "entry %d: runs past payload end", i)
		}
		key := make([]byte, keyLen)
		var tail [12]byte
		if _, err := io.ReadFull(br, key); err != nil {
			return nil, frameErr("payload", "entry %d: key truncated", i)
		}
		if _, err := io.ReadFull(br, tail[:]); err != nil {
			return nil, frameErr("payload", "entry %d: value ref truncated", i)
		}
		off := binary.LittleEndian.Uint64(tail[0:])
		vlen := binary.LittleEndian.Uint32(tail[8:])
		if vlen > MaxValueLen {
			return nil, frameErr("payload", "entry %d: value_len %d", i, vlen)
		}
		if off < DataStart || off > frameOff || uint64(vlen) > frameOff-off {
			return nil, frameErr("payload", "entry %d: value [%d,+%d) outside [%d,%d)", i, off, vlen, DataStart, frameOff)
		}
		if prev != nil && bytes.Compare(prev, key) >= 0 {
			return nil, frameErr("payload", "entry %d: keys not strictly ascending", i)
		}
		prev = key
		pos += 14 + keyLen
		entries = append(entries, KeydirEntry{Key: key, ValueOff: off, ValueLen: vlen})
	}
	if pos != L {
		return nil, frameErr("payload", "%d trailing bytes after %d entries", L-pos, count)
	}
	return entries, nil
}
