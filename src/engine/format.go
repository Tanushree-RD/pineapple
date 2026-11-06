package engine

// Byte-exact on-disk constants (SPEC 4). All integers are little-endian.

import (
	"errors"
	"fmt"
	"hash/crc32"
	"hash/crc64"
)

const (
	SuperblockSlotSize = 4096 // slots A (0..4095) and B (4096..8191)
	SuperblockUsed     = 68   // bytes of a slot that carry data; the rest is zero
	DataStart          = 8192 // offset of frame seq 1

	FrameHeaderLen  = 40
	FrameTrailerLen = 16 // frame_crc (8) + end_magic (8)
	FrameOverhead   = FrameHeaderLen + FrameTrailerLen

	FrameTypeBatch  = 1
	FrameTypeKeydir = 2
	FrameVersion    = 1
	SuperVersion    = 1

	MaxKeyLen      = 1024
	MaxValueLen    = 1 << 20
	MaxOpsPerBatch = 10000
	// MaxPayload = 4 + 10,000 x (7 + 1024 + 1,048,576) = 10,496,070,004 (SPEC 2).
	MaxPayload = 4 + MaxOpsPerBatch*(7+MaxKeyLen+MaxValueLen)
	// MaxPackedPayload bounds one compaction frame's payload (SPEC 2).
	MaxPackedPayload = 4 << 20

	// WriteChunk is the largest single write the frame writer issues (SPEC 4.7).
	WriteChunk = 4 << 20

	batchOpHeaderLen = 7  // kind u8 + key_len u16 + value_len u32
	keydirHeaderLen  = 16 // checkpoint_seq u64 + entry_count u64
	keydirMinEntry   = 15 // 14 + at least a 1 byte key
)

var (
	// Magics are fixed ASCII strings.
	SuperMagic = [8]byte{'L', 'F', 'R', 'O', 'G', '-', 'S', 'B'}
	FrameMagic = [4]byte{'L', 'F', 'R', '1'}
	EndMagic   = [8]byte{'L', 'F', '-', 'E', 'N', 'D', '!', '!'}

	// CRC32C protects the superblock and the frame header; CRC64 (ECMA) protects the frame.
	Crc32cTable = crc32.MakeTable(crc32.Castagnoli)
	Crc64Table  = crc64.MakeTable(crc64.ECMA)
)

// FrameError reports which frame check (SPEC 7.2) failed. Recovery and Check use
// Check to explain exactly why a frame was rejected.
type FrameError struct {
	Check  string // magic, type, version, reserved, header_crc, epoch, seq, length, frame_crc, sentinel, payload, truncated
	Detail string
}

func (e *FrameError) Error() string { return "frame invalid: " + e.Check + ": " + e.Detail }

func frameErr(check, format string, a ...any) error {
	return &FrameError{Check: check, Detail: fmt.Sprintf(format, a...)}
}

// ErrBadSuperblock marks a superblock slot that failed magic, version or sb_crc.
var ErrBadSuperblock = errors.New("invalid superblock slot")

// opSize is the encoded size of one BATCH op (SPEC 4.4).
func opSize(op Op) int64 { return batchOpHeaderLen + int64(len(op.Key)) + int64(len(op.Value)) }

// checkOp validates one op against the SPEC 2 limits.
func checkOp(op Op) error {
	if len(op.Key) < 1 || len(op.Key) > MaxKeyLen {
		return fmt.Errorf("%w: key length %d not in 1..%d", ErrInvalid, len(op.Key), MaxKeyLen)
	}
	if len(op.Value) > MaxValueLen {
		return fmt.Errorf("%w: value length %d exceeds %d", ErrInvalid, len(op.Value), MaxValueLen)
	}
	switch op.Kind {
	case OpPut:
	case OpDelete:
		if len(op.Value) != 0 {
			return fmt.Errorf("%w: delete carries a value", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown op kind %d", ErrInvalid, op.Kind)
	}
	return nil
}
