package tests

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/chethan-9817/pineapple/src/engine"
)

func TestBinaryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "binary.db")

	db, err := engine.Open(dbPath, engine.Options{})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// 1. Extreme characters: null, newline, tab, backslash, invalid UTF-8
	sampleKeys := [][]byte{
		[]byte("key\x00with\x00null"),
		[]byte("key\nwith\nnewlines\n"),
		[]byte("key\twith\ttabs\t"),
		[]byte("key\\with\\backslashes\\"),
		{0xff, 0xfe, 0x80, 0xaa, 0x55, 0x00},
	}

	sampleVals := [][]byte{
		[]byte("val\x00with\x00null"),
		[]byte("val\nwith\nnewlines\n"),
		[]byte("val\twith\ttabs\t"),
		[]byte("val\\with\\backslashes\\"),
		{0xde, 0xad, 0xbe, 0xef, 0x00, 0xff},
	}

	for i := range sampleKeys {
		k := sampleKeys[i]
		v := sampleVals[i]

		if err := db.Put(k, v); err != nil {
			t.Fatalf("Put failed for sample %d: %v", i, err)
		}

		got, found, err := db.Get(k)
		if err != nil || !found {
			t.Fatalf("Get failed for sample %d: found=%v, err=%v", i, found, err)
		}
		if !bytes.Equal(got, v) {
			t.Fatalf("Value mismatch for sample %d:\nGot:  %v\nWant: %v", i, got, v)
		}
	}

	// 2. Maximum limits: exactly 1024-byte key and 1 MiB value
	maxKey := make([]byte, 1024)
	for i := range maxKey {
		maxKey[i] = byte(i % 256)
	}

	maxVal := make([]byte, 1024*1024) // 1 MiB
	for i := range maxVal {
		maxVal[i] = byte((i * 7) % 256)
	}

	if err := db.Put(maxKey, maxVal); err != nil {
		t.Fatalf("Put maxKey/maxVal failed: %v", err)
	}

	gotVal, found, err := db.Get(maxKey)
	if err != nil || !found {
		t.Fatalf("Get maxKey failed: found=%v, err=%v", found, err)
	}
	if !bytes.Equal(gotVal, maxVal) {
		t.Fatalf("Max value mismatch: length got=%d, want=%d", len(gotVal), len(maxVal))
	}

	// 3. Batch round trip with max limits
	key2 := make([]byte, 1024)
	for i := range key2 {
		key2[i] = byte((i + 13) % 256)
	}
	val2 := make([]byte, 1024*1024)
	for i := range val2 {
		val2[i] = byte((i * 11) % 256)
	}

	batch := []engine.Op{
		{Kind: engine.OpPut, Key: key2, Value: val2},
	}
	if err := db.Batch(batch); err != nil {
		t.Fatalf("Batch with 1 MiB value failed: %v", err)
	}

	gotVal2, found, err := db.Get(key2)
	if err != nil || !found || !bytes.Equal(gotVal2, val2) {
		t.Fatalf("Batch 1 MiB value mismatch: found=%v, err=%v", found, err)
	}
}
