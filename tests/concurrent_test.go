package tests

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"math/rand"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chethan-9817/pineapple/src/engine"
)

// TestD4ConcurrentReaderNoPartialBatch tests that a reader reading keys concurrently
// during 10,000-op atomic batches never observes a partial batch.
func TestD4ConcurrentReaderNoPartialBatch(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "d4_atomic.db")

	db, err := engine.Open(dbPath, engine.Options{})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	const numOps = 10000
	const numBatches = 5

	var stopped int32
	var readErrors int64

	// Reader goroutine continuously queries pairs of keys from the batch
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := rand.New(rand.NewSource(42))
		for atomic.LoadInt32(&stopped) == 0 {
			k1Idx := rng.Intn(numOps)
			k2Idx := rng.Intn(numOps)

			k1 := []byte(fmt.Sprintf("d4-key-%05d", k1Idx))
			k2 := []byte(fmt.Sprintf("d4-key-%05d", k2Idx))

			v1, found1, err1 := db.Get(k1)
			v2, found2, err2 := db.Get(k2)

			if err1 != nil || err2 != nil {
				continue
			}

			if found1 != found2 {
				// One found, other not found -> partial batch seen!
				atomic.AddInt64(&readErrors, 1)
			} else if found1 && found2 {
				// Both found: values must match the same batch identifier
				if !bytes.Equal(v1, v2) {
					atomic.AddInt64(&readErrors, 1)
				}
			}
		}
	}()

	// Writer writes multiple 10,000-op batches
	for b := 1; b <= numBatches; b++ {
		batchVal := []byte(fmt.Sprintf("batch-version-%04d", b))
		ops := make([]engine.Op, numOps)
		for i := 0; i < numOps; i++ {
			k := []byte(fmt.Sprintf("d4-key-%05d", i))
			ops[i] = engine.Op{Kind: engine.OpPut, Key: k, Value: batchVal}
		}

		if err := db.Batch(ops); err != nil {
			t.Fatalf("Batch %d failed: %v", b, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	atomic.StoreInt32(&stopped, 1)
	wg.Wait()

	if readErrors > 0 {
		t.Fatalf("D4 violation: concurrent reader observed %d partial batch states!", readErrors)
	}
}

// TestACKOrderMatchesCommitOrder verifies that when two lanes concurrently write
// to the same key via serve, the final value reflects the op whose ACK arrived last.
func TestACKOrderMatchesCommitOrder(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ack_order.db")

	cmd := exec.Command(kvdbBin, "serve", dbPath, "--b64")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdoutPipe)

	// Wait for READY
	readyLine, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(readyLine, "READY ") {
		t.Fatalf("Expected READY, got %s (err: %v)", readyLine, err)
	}

	targetKey := "contended-key"
	targetKeyB64 := base64.StdEncoding.EncodeToString([]byte(targetKey))

	// Run multiple rounds of concurrent lane writes to the same key
	const rounds = 50
	for round := 1; round <= rounds; round++ {
		id1 := uint64(round*10 + 1)
		id2 := uint64(round*10 + 2)

		val1 := fmt.Sprintf("val-lane0-round-%d", round)
		val2 := fmt.Sprintf("val-lane1-round-%d", round)

		val1B64 := base64.StdEncoding.EncodeToString([]byte(val1))
		val2B64 := base64.StdEncoding.EncodeToString([]byte(val2))

		// Send writes from lane 0 and lane 1
		fmt.Fprintf(stdin, "%d 0 PUT %s %s\n", id1, targetKeyB64, val1B64)
		fmt.Fprintf(stdin, "%d 1 PUT %s %s\n", id2, targetKeyB64, val2B64)

		// Read the two ACKs and record which arrived last
		ack1, err1 := reader.ReadString('\n')
		ack2, err2 := reader.ReadString('\n')
		if err1 != nil || err2 != nil {
			t.Fatalf("Failed reading ACKs: %v, %v", err1, err2)
		}

		ack1 = strings.TrimSpace(ack1)
		ack2 = strings.TrimSpace(ack2)

		// Determine which id arrived in ack2 (the last ACK)
		var lastAckId uint64
		parts := strings.Split(ack2, " ")
		if len(parts) >= 2 {
			lastAckId, _ = strconv.ParseUint(parts[1], 10, 64)
		}

		// Verify via GET on lane 0
		getId := uint64(round*10 + 9)
		fmt.Fprintf(stdin, "%d 0 GET %s\n", getId, targetKeyB64)
		getLine, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("Failed reading GET reply: %v", err)
		}

		getLine = strings.TrimSpace(getLine)
		expectedVal := val1
		if lastAckId == id2 {
			expectedVal = val2
		}
		expectedB64 := base64.StdEncoding.EncodeToString([]byte(expectedVal))
		expectedReply := fmt.Sprintf("VAL %d %s", getId, expectedB64)

		if getLine != expectedReply {
			t.Fatalf("Round %d: last ACK was for id %d (expected %q), but GET returned %q (ack1=%q, ack2=%q)",
				round, lastAckId, expectedVal, getLine, ack1, ack2)
		}
	}
}
