package main

import (
	"errors"
	"testing"
)

func TestValueRoundTrip(t *testing.T) {
	key := []byte("user:42")
	for _, size := range []int{12, 13, 31, 32, 33, 100, 4096, 256 << 10, 1 << 20} {
		v := EncodeValue(key, 777, 3, size)
		if len(v) != size {
			t.Fatalf("size %d: got %d bytes", size, len(v))
		}
		info, err := DecodeValue(key, v)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if info.OpID != 777 || info.Size != size || info.Empty {
			t.Fatalf("size %d: bad info %+v", size, info)
		}
		if size >= valueHeaderLen && (!info.HasWriter || info.Writer != 3) {
			t.Fatalf("size %d: writer lost: %+v", size, info)
		}
	}
}

func TestValueEmpty(t *testing.T) {
	v := EncodeValue([]byte("k"), 5, 1, 0)
	if v == nil || len(v) != 0 {
		t.Fatalf("empty value must be non-nil and empty, got %#v", v)
	}
	info, err := DecodeValue([]byte("k"), v)
	if err != nil || !info.Empty {
		t.Fatalf("empty decode: %+v %v", info, err)
	}
}

func TestValueDeterministic(t *testing.T) {
	a := EncodeValue([]byte("k"), 9, 1, 500)
	b := EncodeValue([]byte("k"), 9, 1, 500)
	if string(a) != string(b) {
		t.Fatal("same inputs must give identical bytes")
	}
	c := EncodeValue([]byte("k"), 10, 1, 500)
	if string(a) == string(c) {
		t.Fatal("different op ids must give different bytes")
	}
}

func TestValueRejectsCorruption(t *testing.T) {
	key := []byte("alpha")
	for _, size := range []int{12, 20, 32, 200} {
		good := EncodeValue(key, 1234, 2, size)

		// Flip every single byte, one at a time: nothing may verify.
		for i := range good {
			if size == valueShortMin && i < 8 {
				// A 12-byte value is op_id + 4 key-hash bytes and nothing else, so a flipped
				// op_id still decodes. It decodes to a different op id, which the oracle then
				// rejects because that id is not allowed for the key (see check_test.go).
				continue
			}
			bad := append([]byte(nil), good...)
			bad[i] ^= 0x01
			if _, err := DecodeValue(key, bad); err == nil {
				t.Fatalf("size %d: flip at byte %d went undetected", size, i)
			} else if !errors.Is(err, ErrBadValue) {
				t.Fatalf("error must wrap ErrBadValue: %v", err)
			}
		}
		// Truncation at every length. The long form (>= 32 bytes) carries total_len, so it is
		// always detected. The short form (12..31 bytes) has no length field and its body is a
		// PRNG prefix, so a truncated value still decodes to the same op id but with a smaller
		// Size. The oracle compares Size with the size recorded for that op (check_test.go).
		for n := 1; n < size; n++ {
			info, err := DecodeValue(key, good[:n])
			if size >= valueHeaderLen || n < valueShortMin {
				if err == nil {
					t.Fatalf("size %d: truncation to %d went undetected", size, n)
				}
				continue
			}
			if err != nil || info.Size == size || info.OpID != 1234 {
				t.Fatalf("size %d: truncation to %d: %+v %v", size, n, info, err)
			}
		}
		// Extension by one byte: always detected (the extra byte breaks the PRNG body or total_len).
		if _, err := DecodeValue(key, append(append([]byte(nil), good...), 0)); err == nil {
			t.Fatalf("size %d: extension went undetected", size)
		}
	}
}

func TestValueWrongKey(t *testing.T) {
	for _, size := range []int{12, 31, 32, 64} {
		v := EncodeValue([]byte("key-A"), 1, 1, size)
		if _, err := DecodeValue([]byte("key-B"), v); err == nil {
			t.Fatalf("size %d: value of key-A accepted for key-B", size)
		}
	}
}

func TestValueMixedHalves(t *testing.T) {
	key := []byte("k")
	a := EncodeValue(key, 1, 1, 200)
	b := EncodeValue(key, 2, 1, 200)
	mixed := append(append([]byte(nil), a[:100]...), b[100:]...)
	if _, err := DecodeValue(key, mixed); err == nil {
		t.Fatal("two halves of different ops verified as one value")
	}
}

func TestValueTooShort(t *testing.T) {
	for n := 1; n < valueShortMin; n++ {
		if _, err := DecodeValue([]byte("k"), make([]byte, n)); err == nil {
			t.Fatalf("length %d accepted", n)
		}
	}
}

func TestKeyHashKnownVectors(t *testing.T) {
	// FNV-1a 64 reference vectors.
	if got := keyHash(nil); got != 0xcbf29ce484222325 {
		t.Fatalf("empty: %x", got)
	}
	if got := keyHash([]byte("a")); got != 0xaf63dc4c8601ec8c {
		t.Fatalf("a: %x", got)
	}
	if got := keyHash([]byte("foobar")); got != 0x85944171f73967e8 {
		t.Fatalf("foobar: %x", got)
	}
}

func TestSplitmix64Vector(t *testing.T) {
	// First outputs of splitmix64 seeded with 0 (reference implementation).
	x := uint64(0)
	want := []uint64{0xe220a8397b1dcdaf, 0x6e789e6aa1b965f4, 0x06c45d188009454f}
	for i, w := range want {
		x2 := splitmix64(x)
		if x2 != w {
			t.Fatalf("output %d: got %x want %x", i, x2, w)
		}
		x += 0x9e3779b97f4a7c15
	}
}
