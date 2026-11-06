package main

// HYDRA's own, independent reader and writer of the LEAPFROG-R on-disk format (SPEC 4, 7.2, 7.3).
//
// It is written from the SPEC tables, not from the engine's code, for two reasons:
//   - the fixtures mode (SPEC 15.8) must cut and corrupt a real file at exact offsets, and then
//     know what a correct database has to say about the result;
//   - if this reader and the engine disagree about a real file, one of them is wrong, which is
//     exactly the kind of bug a torture tool exists to find.
//
// All integers are little-endian. CRC32C is the Castagnoli table, CRC64 is the ECMA table.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"hash/crc64"
	"sort"
)

const (
	sbMagic       = "LFROG-SB"
	frameMagic    = "LFR1"
	endMagic      = "LF-END!!"
	sbSlotSize    = 4096
	sbUsed        = 68
	firstFrameOff = 8192
	frameHdrLen   = 40
	frameTrailLen = 16
	typeBatch     = 1
	typeKeydir    = 2
	maxKeyLen     = 1024
	maxValueLen   = 1 << 20
	maxBatchOps   = 10000
	// MAX_PAYLOAD = 4 + 10,000 x (7 + 1024 + 1,048,576) (SPEC 2)
	maxPayload = 4 + maxBatchOps*(7+maxKeyLen+maxValueLen)
)

var crc64Table = crc64.MakeTable(crc64.ECMA)

// SuperSlot is one decoded superblock slot (SPEC 4.2).
type SuperSlot struct {
	Valid       bool
	Generation  uint64
	Epoch       uint64
	CkptOff     uint64
	CkptSeq     uint64
	ReplayStart uint64
	MaxBytes    uint64
}

// EncodeSuperblock returns the full 4096-byte slot.
func EncodeSuperblock(s SuperSlot) []byte {
	b := make([]byte, sbSlotSize)
	copy(b[0:8], sbMagic)
	binary.LittleEndian.PutUint32(b[8:12], 1) // version
	binary.LittleEndian.PutUint64(b[16:24], s.Generation)
	binary.LittleEndian.PutUint64(b[24:32], s.Epoch)
	binary.LittleEndian.PutUint64(b[32:40], s.CkptOff)
	binary.LittleEndian.PutUint64(b[40:48], s.CkptSeq)
	binary.LittleEndian.PutUint64(b[48:56], s.ReplayStart)
	binary.LittleEndian.PutUint64(b[56:64], s.MaxBytes)
	binary.LittleEndian.PutUint32(b[64:68], crc32.Checksum(b[0:64], castagnoli))
	return b
}

// ParseSuperblock decodes a slot. A slot is valid when magic, version and sb_crc all match.
func ParseSuperblock(b []byte) SuperSlot {
	if len(b) < sbUsed || string(b[0:8]) != sbMagic || binary.LittleEndian.Uint32(b[8:12]) != 1 ||
		binary.LittleEndian.Uint32(b[64:68]) != crc32.Checksum(b[0:64], castagnoli) {
		return SuperSlot{}
	}
	return SuperSlot{
		Valid:       true,
		Generation:  binary.LittleEndian.Uint64(b[16:24]),
		Epoch:       binary.LittleEndian.Uint64(b[24:32]),
		CkptOff:     binary.LittleEndian.Uint64(b[32:40]),
		CkptSeq:     binary.LittleEndian.Uint64(b[40:48]),
		ReplayStart: binary.LittleEndian.Uint64(b[48:56]),
		MaxBytes:    binary.LittleEndian.Uint64(b[56:64]),
	}
}

// FileOp is one decoded BATCH op.
type FileOp struct {
	Del      bool
	Key      []byte
	Value    []byte
	ValueOff int64 // absolute file offset of the value bytes (SPEC 4.6); 0 for deletes
}

// KeydirEntry is one KEYDIR entry (SPEC 4.5).
type KeydirEntry struct {
	Key      []byte
	ValueOff uint64
	ValueLen uint32
}

// EncodeBatchFrame builds one BATCH frame (SPEC 4.3, 4.4).
func EncodeBatchFrame(epoch, seq uint64, ops []FileOp) []byte {
	p := binary.LittleEndian.AppendUint32(nil, uint32(len(ops)))
	for _, o := range ops {
		kind := byte(1)
		if o.Del {
			kind = 2
		}
		p = append(p, kind)
		p = binary.LittleEndian.AppendUint16(p, uint16(len(o.Key)))
		p = binary.LittleEndian.AppendUint32(p, uint32(len(o.Value)))
		p = append(p, o.Key...)
		p = append(p, o.Value...)
	}
	return encodeFrame(typeBatch, epoch, seq, p)
}

// EncodeKeydirFrame builds one KEYDIR frame (SPEC 4.5). Entries must already be sorted.
func EncodeKeydirFrame(epoch, seq uint64, entries []KeydirEntry) []byte {
	p := binary.LittleEndian.AppendUint64(nil, seq) // checkpoint_seq = this frame's seq
	p = binary.LittleEndian.AppendUint64(p, uint64(len(entries)))
	for _, e := range entries {
		p = binary.LittleEndian.AppendUint16(p, uint16(len(e.Key)))
		p = append(p, e.Key...)
		p = binary.LittleEndian.AppendUint64(p, e.ValueOff)
		p = binary.LittleEndian.AppendUint32(p, e.ValueLen)
	}
	return encodeFrame(typeKeydir, epoch, seq, p)
}

func encodeFrame(typ byte, epoch, seq uint64, payload []byte) []byte {
	f := make([]byte, frameHdrLen, frameHdrLen+len(payload)+frameTrailLen)
	copy(f[0:4], frameMagic)
	f[4], f[5] = typ, 1
	binary.LittleEndian.PutUint64(f[8:16], epoch)
	binary.LittleEndian.PutUint64(f[16:24], seq)
	binary.LittleEndian.PutUint64(f[24:32], uint64(len(payload)))
	binary.LittleEndian.PutUint32(f[32:36], crc32.Checksum(f[0:32], castagnoli))
	f = append(f, payload...)
	f = binary.LittleEndian.AppendUint64(f, crc64.Checksum(f, crc64Table))
	return append(f, endMagic...)
}

// FrameInfo describes one accepted frame.
type FrameInfo struct {
	Off           int64
	Type          byte
	Seq           uint64
	PayloadLen    uint64
	End           int64 // Off + 56 + L; may be past EOF for a short-sentinel frame
	Ops           []FileOp
	ShortSentinel bool
}

// Image is a decoded database file.
type Image struct {
	Slots      [2]SuperSlot
	Chosen     int // index of the slot recovery uses
	Super      SuperSlot
	Frames     []FrameInfo
	LogicalEnd int64
	Tail       string // COMPLETE | TORN | SHORT_SENTINEL
	StopReason string // why the scan stopped (empty when the file ended exactly on a frame)
	State      map[string][]byte
}

// ReadImage decodes data exactly as SPEC 7 describes, with a FULL scan from offset 8192 (it never
// trusts a checkpoint to skip frames). It returns an error only when no superblock slot is valid.
func ReadImage(data []byte) (*Image, error) {
	im := &Image{State: map[string][]byte{}}
	for i := 0; i < 2; i++ {
		if len(data) >= (i+1)*sbSlotSize {
			im.Slots[i] = ParseSuperblock(data[i*sbSlotSize : (i+1)*sbSlotSize])
		}
	}
	switch {
	case !im.Slots[0].Valid && !im.Slots[1].Valid:
		return nil, fmt.Errorf("no valid superblock slot")
	case im.Slots[0].Valid && im.Slots[1].Valid && im.Slots[1].Generation > im.Slots[0].Generation:
		im.Chosen = 1
	case !im.Slots[0].Valid:
		im.Chosen = 1
	}
	im.Super = im.Slots[im.Chosen]

	off, seq := int64(firstFrameOff), uint64(1)
	im.LogicalEnd = off
	im.Tail = "COMPLETE"
	for off < int64(len(data)) {
		fr, reason := parseFrame(data, off, im.Super.Epoch, seq)
		if fr == nil {
			im.Tail, im.StopReason = "TORN", reason
			return im, nil
		}
		im.Frames = append(im.Frames, *fr)
		if fr.Type == typeBatch {
			for _, o := range fr.Ops {
				if o.Del {
					delete(im.State, string(o.Key))
				} else {
					im.State[string(o.Key)] = o.Value
				}
			}
		}
		if fr.ShortSentinel {
			im.Tail, im.LogicalEnd = "SHORT_SENTINEL", fr.End
			return im, nil
		}
		off, seq = fr.End, seq+1
		im.LogicalEnd = off
	}
	return im, nil
}

// parseFrame applies the checks of SPEC 7.2, in order. It returns nil and a reason when the frame
// is not valid. It never allocates before the length checks pass.
func parseFrame(data []byte, off int64, epoch, wantSeq uint64) (*FrameInfo, string) {
	r := int64(len(data)) - off
	if r < frameHdrLen {
		return nil, fmt.Sprintf("only %d bytes remain, a header needs 40", r)
	}
	h := data[off : off+frameHdrLen]
	if string(h[0:4]) != frameMagic {
		return nil, "bad frame magic"
	}
	typ := h[4]
	if typ != typeBatch && typ != typeKeydir {
		return nil, fmt.Sprintf("bad frame type %d", typ)
	}
	if h[5] != 1 {
		return nil, fmt.Sprintf("bad frame version %d", h[5])
	}
	if h[6] != 0 || h[7] != 0 || binary.LittleEndian.Uint32(h[36:40]) != 0 {
		return nil, "reserved header bytes are not zero"
	}
	if binary.LittleEndian.Uint32(h[32:36]) != crc32.Checksum(h[0:32], castagnoli) {
		return nil, "header_crc mismatch"
	}
	if binary.LittleEndian.Uint64(h[8:16]) != epoch {
		return nil, "epoch mismatch"
	}
	if binary.LittleEndian.Uint64(h[16:24]) != wantSeq {
		return nil, fmt.Sprintf("seq %d, expected %d", binary.LittleEndian.Uint64(h[16:24]), wantSeq)
	}
	l := binary.LittleEndian.Uint64(h[24:32])
	if l > maxPayload || uint64(r) < 48+l {
		return nil, "payload_len out of bounds or frame truncated before frame_crc"
	}
	body := data[off : off+int64(frameHdrLen+l)]
	crcAt := off + int64(frameHdrLen+l)
	if binary.LittleEndian.Uint64(data[crcAt:crcAt+8]) != crc64.Checksum(body, crc64Table) {
		return nil, "frame_crc mismatch"
	}
	fr := &FrameInfo{Off: off, Type: typ, Seq: wantSeq, PayloadLen: l, End: off + int64(56+l)}
	if uint64(r) >= 56+l {
		if string(data[crcAt+8:crcAt+16]) != endMagic {
			return nil, "end_magic mismatch"
		}
	} else {
		have := data[crcAt+8:]
		if !bytes.HasPrefix([]byte(endMagic), have) {
			return nil, "partial end_magic is not a prefix of LF-END!!"
		}
		fr.ShortSentinel = true
	}
	payload := data[off+frameHdrLen : off+int64(frameHdrLen+l)]
	var err error
	if typ == typeBatch {
		fr.Ops, err = parseBatchPayload(payload, off)
	} else {
		err = checkKeydirPayload(payload, wantSeq, off)
	}
	if err != nil {
		return nil, err.Error()
	}
	return fr, ""
}

func parseBatchPayload(p []byte, frameOff int64) ([]FileOp, error) {
	if len(p) < 4 {
		return nil, fmt.Errorf("batch payload shorter than op_count")
	}
	n := binary.LittleEndian.Uint32(p[0:4])
	if n < 1 || n > maxBatchOps {
		return nil, fmt.Errorf("op_count %d out of range", n)
	}
	pos := 4
	ops := make([]FileOp, 0, n)
	for i := uint32(0); i < n; i++ {
		if len(p)-pos < 7 {
			return nil, fmt.Errorf("op %d header truncated", i)
		}
		kind := p[pos]
		kl := int(binary.LittleEndian.Uint16(p[pos+1 : pos+3]))
		vl := int(binary.LittleEndian.Uint32(p[pos+3 : pos+7]))
		if kind != 1 && kind != 2 {
			return nil, fmt.Errorf("op %d bad kind %d", i, kind)
		}
		if kl < 1 || kl > maxKeyLen || vl > maxValueLen || (kind == 2 && vl != 0) {
			return nil, fmt.Errorf("op %d bad key_len %d / value_len %d", i, kl, vl)
		}
		if len(p)-pos-7 < kl+vl {
			return nil, fmt.Errorf("op %d body truncated", i)
		}
		o := FileOp{Del: kind == 2, Key: p[pos+7 : pos+7+kl]}
		if kind == 1 {
			o.Value = p[pos+7+kl : pos+7+kl+vl]
			o.ValueOff = frameOff + frameHdrLen + int64(pos) + 7 + int64(kl)
		}
		ops = append(ops, o)
		pos += 7 + kl + vl
	}
	if pos != len(p) {
		return nil, fmt.Errorf("%d unparsed bytes after the last op", len(p)-pos)
	}
	return ops, nil
}

func checkKeydirPayload(p []byte, seq uint64, frameOff int64) error {
	if len(p) < 16 {
		return fmt.Errorf("keydir payload shorter than its header")
	}
	if binary.LittleEndian.Uint64(p[0:8]) != seq {
		return fmt.Errorf("keydir checkpoint_seq %d != frame seq %d", binary.LittleEndian.Uint64(p[0:8]), seq)
	}
	n := binary.LittleEndian.Uint64(p[8:16])
	pos := 16
	var prev []byte
	for i := uint64(0); i < n; i++ {
		if len(p)-pos < 2 {
			return fmt.Errorf("keydir entry %d truncated", i)
		}
		kl := int(binary.LittleEndian.Uint16(p[pos : pos+2]))
		if kl < 1 || kl > maxKeyLen || len(p)-pos-2 < kl+12 {
			return fmt.Errorf("keydir entry %d bad key_len %d", i, kl)
		}
		key := p[pos+2 : pos+2+kl]
		vo := binary.LittleEndian.Uint64(p[pos+2+kl : pos+10+kl])
		vl := uint64(binary.LittleEndian.Uint32(p[pos+10+kl : pos+14+kl]))
		if vl > maxValueLen || vo < firstFrameOff || vo+vl > uint64(frameOff) {
			return fmt.Errorf("keydir entry %d value range [%d,+%d) outside [8192,%d)", i, vo, vl, frameOff)
		}
		if prev != nil && bytes.Compare(prev, key) >= 0 {
			return fmt.Errorf("keydir entry %d not strictly ascending", i)
		}
		prev = key
		pos += 14 + kl
	}
	if pos != len(p) {
		return fmt.Errorf("%d unparsed bytes after the last keydir entry", len(p)-pos)
	}
	return nil
}

// SortedKeys returns the image's live keys in ascending byte order (the order `kvdb dump` prints).
func (im *Image) SortedKeys() []string {
	ks := make([]string, 0, len(im.State))
	for k := range im.State {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
