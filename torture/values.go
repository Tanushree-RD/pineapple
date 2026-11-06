package main

// Self-describing values (SPEC 14.3).
//
// Every value HYDRA writes names exactly one operation, proves which key it was
// written for, and proves it is not torn or mixed. So an observed value can be
// traced back to the single op that wrote it, with no help from the database.
//
//	>= 32 bytes: "HYDR"(4) op_id u64 key_hash u64 writer u32 total_len u32 hdr_crc u32 | PRNG bytes
//	12..31 bytes: op_id u64, low 32 bits of key_hash | PRNG bytes
//	empty:        only for an explicit "empty value" op; nothing to decode
//
// All integers are little-endian. hdr_crc is CRC32C of the previous 28 bytes.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

const (
	valueMagic      = "HYDR"
	valueHeaderLen  = 32 // size of the long-form header
	valueShortMin   = 12 // smallest non-empty value HYDRA ever writes
	valueMaxLen     = 1 << 20
	valueHdrCRCSpan = 28
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// splitmix64 is the generator named in SPEC 15.3. It is also used to fill value bodies.
func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	z := x
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// keyHash is FNV-1a 64 of the key (SPEC 14.3).
func keyHash(key []byte) uint64 {
	h := uint64(14695981039346656037)
	for _, b := range key {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return h
}

// fillPRNG writes deterministic pseudo-random bytes derived from seed into dst.
func fillPRNG(dst []byte, seed uint64) {
	state := seed
	for i := 0; i < len(dst); {
		state = splitmix64(state)
		var w [8]byte
		binary.LittleEndian.PutUint64(w[:], state)
		i += copy(dst[i:], w[:])
	}
}

// EncodeValue builds the value HYDRA writes for (key, opID, writer) of the given size.
// size 0 returns an empty (non-nil) value. Sizes 1..11 and sizes above 1 MiB are programming errors.
func EncodeValue(key []byte, opID uint64, writer uint32, size int) []byte {
	if size == 0 {
		return []byte{}
	}
	if size < valueShortMin || size > valueMaxLen {
		panic(fmt.Sprintf("hydra: bad value size %d", size))
	}
	v := make([]byte, size)
	kh := keyHash(key)
	if size >= valueHeaderLen {
		copy(v[0:4], valueMagic)
		binary.LittleEndian.PutUint64(v[4:12], opID)
		binary.LittleEndian.PutUint64(v[12:20], kh)
		binary.LittleEndian.PutUint32(v[20:24], writer)
		binary.LittleEndian.PutUint32(v[24:28], uint32(size))
		binary.LittleEndian.PutUint32(v[28:32], crc32.Checksum(v[:valueHdrCRCSpan], castagnoli))
		fillPRNG(v[valueHeaderLen:], opID)
		return v
	}
	binary.LittleEndian.PutUint64(v[0:8], opID)
	binary.LittleEndian.PutUint32(v[8:12], uint32(kh))
	fillPRNG(v[12:], opID)
	return v
}

// ValueInfo is what a verified value proves.
type ValueInfo struct {
	Empty     bool   // an empty value (no op id can be recovered)
	OpID      uint64 // the op that wrote it
	Writer    uint32 // lane/writer number; only meaningful when HasWriter
	HasWriter bool   // long-form values carry the writer
	Size      int
}

// ErrBadValue wraps every reason a value fails verification.
var ErrBadValue = errors.New("bad value")

// DecodeValue verifies v against key and returns what it proves. Any torn, mixed,
// foreign-key or garbage value returns an error wrapping ErrBadValue.
func DecodeValue(key, v []byte) (ValueInfo, error) {
	n := len(v)
	if n == 0 {
		return ValueInfo{Empty: true}, nil
	}
	if n < valueShortMin {
		return ValueInfo{}, fmt.Errorf("%w: length %d is below the %d-byte minimum", ErrBadValue, n, valueShortMin)
	}
	kh := keyHash(key)
	if n >= valueHeaderLen {
		if !bytes.Equal(v[0:4], []byte(valueMagic)) {
			return ValueInfo{}, fmt.Errorf("%w: missing HYDR magic", ErrBadValue)
		}
		if got, want := binary.LittleEndian.Uint32(v[28:32]), crc32.Checksum(v[:valueHdrCRCSpan], castagnoli); got != want {
			return ValueInfo{}, fmt.Errorf("%w: header crc mismatch", ErrBadValue)
		}
		if tl := binary.LittleEndian.Uint32(v[24:28]); int(tl) != n {
			return ValueInfo{}, fmt.Errorf("%w: total_len %d but value is %d bytes (torn or mixed)", ErrBadValue, tl, n)
		}
		if got := binary.LittleEndian.Uint64(v[12:20]); got != kh {
			return ValueInfo{}, fmt.Errorf("%w: key_hash mismatch (value belongs to another key)", ErrBadValue)
		}
		opID := binary.LittleEndian.Uint64(v[4:12])
		if err := checkBody(v[valueHeaderLen:], opID); err != nil {
			return ValueInfo{}, err
		}
		return ValueInfo{OpID: opID, Writer: binary.LittleEndian.Uint32(v[20:24]), HasWriter: true, Size: n}, nil
	}
	opID := binary.LittleEndian.Uint64(v[0:8])
	if got := binary.LittleEndian.Uint32(v[8:12]); got != uint32(kh) {
		return ValueInfo{}, fmt.Errorf("%w: key_hash mismatch (value belongs to another key)", ErrBadValue)
	}
	if err := checkBody(v[12:], opID); err != nil {
		return ValueInfo{}, err
	}
	return ValueInfo{OpID: opID, Size: n}, nil
}

func checkBody(body []byte, opID uint64) error {
	want := make([]byte, len(body))
	fillPRNG(want, opID)
	if !bytes.Equal(body, want) {
		return fmt.Errorf("%w: body does not match PRNG stream of op %d (torn or mixed)", ErrBadValue, opID)
	}
	return nil
}
