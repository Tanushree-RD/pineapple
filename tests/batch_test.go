package tests

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/chethan-9817/pineapple/src/engine"
)

func TestBatchB5Ordering(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "b5.db")

	db, err := engine.Open(dbPath, engine.Options{})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	keyX := []byte("key-x")
	val1 := []byte("val-1")
	val2 := []byte("val-2")

	// Case 1: PUT X then DEL X in same batch -> X must be absent
	batch1 := []engine.Op{
		{Kind: engine.OpPut, Key: keyX, Value: val1},
		{Kind: engine.OpDelete, Key: keyX},
	}
	if err := db.Batch(batch1); err != nil {
		t.Fatalf("Batch 1 failed: %v", err)
	}

	val, found, err := db.Get(keyX)
	if err != nil {
		t.Fatalf("Get keyX failed: %v", err)
	}
	if found {
		t.Fatalf("Expected keyX to be absent after PUT then DEL, got found=true with val=%q", val)
	}

	// Case 2: DEL X then PUT X in same batch -> X must be present with val2
	batch2 := []engine.Op{
		{Kind: engine.OpDelete, Key: keyX},
		{Kind: engine.OpPut, Key: keyX, Value: val2},
	}
	if err := db.Batch(batch2); err != nil {
		t.Fatalf("Batch 2 failed: %v", err)
	}

	val, found, err = db.Get(keyX)
	if err != nil {
		t.Fatalf("Get keyX failed: %v", err)
	}
	if !found {
		t.Fatalf("Expected keyX to be present after DEL then PUT, got found=false")
	}
	if !bytes.Equal(val, val2) {
		t.Fatalf("Expected keyX value %q, got %q", val2, val)
	}
}

func TestEmptyValueVsDelete(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "empty_vs_del.db")

	db, err := engine.Open(dbPath, engine.Options{})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	k := []byte("k-empty")

	// 1. PUT key with empty value
	if err := db.Put(k, []byte{}); err != nil {
		t.Fatalf("Put with empty value failed: %v", err)
	}

	val, found, err := db.Get(k)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !found {
		t.Fatalf("Expected key with empty value to be found=true, got false")
	}
	if len(val) != 0 {
		t.Fatalf("Expected empty value with len 0, got %v", val)
	}

	// In ForEach, empty value must be visited
	var visitedCount int
	err = db.ForEach(func(key, value []byte) error {
		if bytes.Equal(key, k) {
			visitedCount++
			if len(value) != 0 {
				t.Fatalf("Expected empty value in ForEach, got %v", value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ForEach failed: %v", err)
	}
	if visitedCount != 1 {
		t.Fatalf("Expected empty key to be visited once, got %d", visitedCount)
	}

	// 2. DELETE key
	if err := db.Delete(k); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	val, found, err = db.Get(k)
	if err != nil {
		t.Fatalf("Get after delete failed: %v", err)
	}
	if found {
		t.Fatalf("Expected key after Delete to have found=false, got true with val=%v", val)
	}

	// In ForEach, deleted key must NOT be visited
	visitedCount = 0
	_ = db.ForEach(func(key, value []byte) error {
		if bytes.Equal(key, k) {
			visitedCount++
		}
		return nil
	})
	if visitedCount != 0 {
		t.Fatalf("Expected deleted key to not be visited in ForEach, got %d", visitedCount)
	}
}
