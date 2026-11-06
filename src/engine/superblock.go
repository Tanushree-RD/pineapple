package engine

// Superblock slots (SPEC 4.2).
//
//	0  magic "LFROG-SB" (8)   8 version=1 (4)   12 reserved (4)
//	16 generation (8)         24 epoch (8)       32 ckpt_off (8)
//	40 ckpt_seq (8)           48 replay_start (8) 56 max_bytes (8)
//	64 sb_crc = CRC32C(bytes 0..63) (4)          68..4095 zero

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
)

type Superblock struct {
	Generation  uint64
	Epoch       uint64
	CkptOff     uint64 // absolute offset of the checkpoint KEYDIR frame, 0 = none
	CkptSeq     uint64 // seq of that frame; KEYDIR represents every seq < CkptSeq
	ReplayStart uint64 // CkptOff + 56 + L of that frame; 0 = none
	MaxBytes    uint64 // persisted size cap, 0 = no cap
}

// Marshal returns the full 4096-byte slot.
func (s Superblock) Marshal() []byte {
	b := make([]byte, SuperblockSlotSize)
	copy(b[0:8], SuperMagic[:])
	binary.LittleEndian.PutUint32(b[8:], SuperVersion)
	// 12..15 reserved = 0
	binary.LittleEndian.PutUint64(b[16:], s.Generation)
	binary.LittleEndian.PutUint64(b[24:], s.Epoch)
	binary.LittleEndian.PutUint64(b[32:], s.CkptOff)
	binary.LittleEndian.PutUint64(b[40:], s.CkptSeq)
	binary.LittleEndian.PutUint64(b[48:], s.ReplayStart)
	binary.LittleEndian.PutUint64(b[56:], s.MaxBytes)
	binary.LittleEndian.PutUint32(b[64:], crc32.Checksum(b[0:64], Crc32cTable))
	return b
}

// ParseSuperblock decodes a slot. A slot is valid when magic, version and sb_crc all
// match; anything else returns ErrBadSuperblock (a torn slot is simply not chosen).
func ParseSuperblock(b []byte) (Superblock, error) {
	if len(b) < SuperblockUsed {
		return Superblock{}, fmt.Errorf("%w: only %d bytes", ErrBadSuperblock, len(b))
	}
	if [8]byte(b[0:8]) != SuperMagic {
		return Superblock{}, fmt.Errorf("%w: bad magic", ErrBadSuperblock)
	}
	if v := binary.LittleEndian.Uint32(b[8:]); v != SuperVersion {
		return Superblock{}, fmt.Errorf("%w: version %d", ErrBadSuperblock, v)
	}
	if crc32.Checksum(b[0:64], Crc32cTable) != binary.LittleEndian.Uint32(b[64:]) {
		return Superblock{}, fmt.Errorf("%w: sb_crc mismatch", ErrBadSuperblock)
	}
	return Superblock{
		Generation:  binary.LittleEndian.Uint64(b[16:]),
		Epoch:       binary.LittleEndian.Uint64(b[24:]),
		CkptOff:     binary.LittleEndian.Uint64(b[32:]),
		CkptSeq:     binary.LittleEndian.Uint64(b[40:]),
		ReplayStart: binary.LittleEndian.Uint64(b[48:]),
		MaxBytes:    binary.LittleEndian.Uint64(b[56:]),
	}, nil
}

// Next returns the superblock to write into the INACTIVE slot: generation + 1 with
// every other field copied. Generation overflow is rejected (SPEC 4.2).
func (s Superblock) Next() (Superblock, error) {
	if s.Generation == math.MaxUint64 {
		return Superblock{}, fmt.Errorf("%w: generation overflow", ErrCorrupt)
	}
	s.Generation++
	return s, nil
}

// ChooseSuperblock picks the valid slot with the highest generation (CP-R01).
// idx is 0 for slot A, 1 for slot B. If neither slot is valid it returns ErrCorrupt:
// our crash model cannot produce that, so we never reinitialize (SPEC 7.1 step 4).
func ChooseSuperblock(slotA, slotB []byte) (idx int, sb Superblock, err error) {
	a, errA := ParseSuperblock(slotA)
	b, errB := ParseSuperblock(slotB)
	switch {
	case errA != nil && errB != nil:
		return 0, Superblock{}, fmt.Errorf("%w: no valid superblock (A: %v; B: %v)", ErrCorrupt, errA, errB)
	case errA != nil:
		return 1, b, nil
	case errB != nil:
		return 0, a, nil
	case b.Generation > a.Generation:
		return 1, b, nil
	default: // equal generations cannot happen; A wins deterministically
		return 0, a, nil
	}
}

// NewEpoch returns a random non-zero 64-bit image id.
func NewEpoch() (uint64, error) {
	for {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		if e := binary.LittleEndian.Uint64(b[:]); e != 0 {
			return e, nil
		}
	}
}

// InitialImage is the 8192-byte image of a brand-new database (SPEC 6.1):
// slot A valid with generation 1 and no checkpoint, slot B all zeros.
func InitialImage(epoch, maxBytes uint64) []byte {
	img := make([]byte, DataStart)
	copy(img, Superblock{Generation: 1, Epoch: epoch, MaxBytes: maxBytes}.Marshal())
	return img
}
