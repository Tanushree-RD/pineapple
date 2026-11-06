package engine

import (
	"errors"
	"fmt"
	"math/rand"
	"sync/atomic"
	"testing"
)

const testCap = 300_000

func capped(t *testing.T) (*DB, string) {
	t.Helper()
	p := newPath(t)
	return open(t, p, Options{MaxBytes: testCap, SetMaxBytes: true}), p
}

func TestE5DeleteAtTheCapRewritesAndPutsWorkAgain(t *testing.T) {
	db, p := capped(t)
	defer db.Close()
	val := make([]byte, 2000)
	n := 0
	for ; ; n++ { // fill until the cap refuses
		if err := db.Put([]byte(fmt.Sprintf("k%04d", n)), val); err != nil {
			if !errors.Is(err, ErrFull) {
				t.Fatal(err)
			}
			break
		}
		if fileSize(t, p) > testCap {
			t.Fatalf("file %d exceeds the cap", fileSize(t, p))
		}
	}
	if n < 50 {
		t.Fatalf("only %d puts before ErrFull", n)
	}
	sizeFull := fileSize(t, p)
	// Deleting needs no tombstone space: the rewrite simply omits the key.
	for i := 0; i < n/2; i++ {
		if err := db.Delete([]byte(fmt.Sprintf("k%04d", i))); err != nil {
			t.Fatalf("delete %d at the cap: %v", i, err)
		}
		if fileSize(t, p) > testCap {
			t.Fatalf("file %d exceeds the cap after a delete", fileSize(t, p))
		}
	}
	if fileSize(t, p) >= sizeFull {
		t.Fatalf("image did not shrink: %d -> %d", sizeFull, fileSize(t, p))
	}
	// And puts append again.
	for i := 0; i < 10; i++ {
		if err := db.Put([]byte(fmt.Sprintf("new%d", i)), val); err != nil {
			t.Fatalf("put after reclaiming: %v", err)
		}
	}
	db.Close()
	db = open(t, p)
	if _, ok := mustGet(t, db, "k0000"); ok {
		t.Fatal("deleted key resurfaced")
	}
	if _, ok := mustGet(t, db, fmt.Sprintf("k%04d", n-1)); !ok {
		t.Fatal("kept key lost")
	}
	db.Close() // Check needs the lock
	rep, _ := Check(p)
	if !rep.OK {
		t.Fatalf("%+v", rep)
	}
}

func TestRewriteReclaimsGarbageForAPut(t *testing.T) {
	// Lots of overwritten (dead) bytes: the file is at the cap, but the live set is small,
	// so a put takes the REWRITE branch and succeeds.
	db, p := capped(t)
	val := make([]byte, 3000)
	for i := 0; fileSize(t, p) < testCap-4000; i++ {
		if err := db.Put([]byte("hot"), val); err != nil { // overwrite the same key
			t.Fatal(err)
		}
	}
	var hooks atomic.Int32
	db.opts.OnCommit = func(uint64) { hooks.Add(1) }
	before := fileSize(t, p)
	if err := db.Apply([]Op{{Kind: OpPut, Key: []byte("fresh"), Value: make([]byte, 5000)}}, 7); err != nil {
		t.Fatalf("put that needs a rewrite: %v", err)
	}
	if hooks.Load() != 1 {
		t.Fatalf("OnCommit ran %d times for the rewrite", hooks.Load())
	}
	if fileSize(t, p) >= before {
		t.Fatalf("rewrite did not shrink the image: %d -> %d", before, fileSize(t, p))
	}
	db.Close()
	db = open(t, p)
	defer db.Close()
	if v, ok, _ := db.Get([]byte("fresh")); !ok || len(v) != 5000 {
		t.Fatal("the transaction that triggered the rewrite is not durable")
	}
	if v, ok, _ := db.Get([]byte("hot")); !ok || len(v) != 3000 {
		t.Fatal("live key lost by the rewrite")
	}
}

func TestRewriteAppliesTheWholeBatchInOrder(t *testing.T) {
	db, p := capped(t)
	defer db.Close()
	val := make([]byte, 3000)
	db.Put([]byte("keep"), []byte("1"))
	db.Put([]byte("drop"), []byte("1"))
	for fileSize(t, p) < testCap-3500 {
		db.Put([]byte("hot"), val)
	}
	err := db.Batch([]Op{
		{Kind: OpPut, Key: []byte("X"), Value: []byte("a")},
		{Kind: OpDelete, Key: []byte("X")}, // B5 inside a rewrite: PUT X; DEL X
		{Kind: OpDelete, Key: []byte("drop")},
		{Kind: OpPut, Key: []byte("Y"), Value: val},
		{Kind: OpDelete, Key: []byte("Z")},
		{Kind: OpPut, Key: []byte("Z"), Value: []byte("z")}, // DEL Z; PUT Z
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		k    string
		want bool
	}{{"X", false}, {"drop", false}, {"keep", true}, {"Y", true}, {"Z", true}, {"hot", true}} {
		if _, ok := mustGet(t, db, c.k); ok != c.want {
			t.Fatalf("%s present=%v want %v", c.k, ok, c.want)
		}
	}
}

func TestHeadroomGuardRefusesAPutThatWouldLeaveNoRoom(t *testing.T) {
	// Live data alone nearly fills the cap: the image would fit by a few bytes, but
	// S + H > max_bytes, so a PUT gets ErrFull (SPEC 8.3, weakness 2) while a delete works.
	p := newPath(t)
	db := open(t, p, Options{MaxBytes: 200_000, SetMaxBytes: true})
	defer db.Close()
	val := make([]byte, 1000)
	n := 0
	for ; ; n++ {
		if err := db.Put([]byte(fmt.Sprintf("k%04d", n)), val); err != nil {
			break
		}
	}
	for i := 0; ; i++ { // squeeze in tiny puts until even a tiny APPEND no longer fits
		if err := db.Put([]byte(fmt.Sprintf("tiny%d", i)), nil); err != nil {
			break
		}
	}
	if err := db.Put([]byte("one-more"), []byte("x")); !errors.Is(err, ErrFull) {
		t.Fatalf("put near the cap: %v", err)
	}
	before := fileSize(t, p)
	if err := db.Delete([]byte("k0000")); err != nil {
		t.Fatalf("delete near the cap must work: %v", err)
	}
	if fileSize(t, p) > before {
		t.Fatal("delete grew the file")
	}
	if err := db.Put([]byte("one-more"), []byte("x")); err != nil {
		t.Fatalf("after the delete made room: %v", err)
	}
}

func TestStreamNearTheCapOnlyAppends(t *testing.T) {
	db, p := capped(t)
	defer db.Close()
	val := make([]byte, 3000)
	for fileSize(t, p) < testCap-3500 {
		db.Put([]byte("hot"), val)
	}
	ops := []Op{{Kind: OpPut, Key: []byte("s"), Value: make([]byte, 5000)}}
	size, _ := BatchPayloadSize(ops)
	if err := db.ApplyStream(1, size, streamOf(ops), 0); !errors.Is(err, ErrFull) {
		t.Fatalf("a streamed batch cannot rewrite: %v", err)
	}
	// The same content through the buffered path succeeds (SPEC 17 item 5).
	if err := db.Batch(ops); err != nil {
		t.Fatalf("buffered path: %v", err)
	}
}

func TestDeleteOnlyThatCannotShrinkEnoughIsFull(t *testing.T) {
	// A tiny cap below even the minimal live image: nothing can be done, and nothing is touched.
	p := newPath(t)
	db := open(t, p)
	for i := 0; i < 20; i++ {
		db.Put([]byte(fmt.Sprintf("k%d", i)), make([]byte, 1000))
	}
	db.Close()
	db = open(t, p, Options{MaxBytes: 9000, SetMaxBytes: true}) // below the file already
	defer db.Close()
	before := fileSize(t, p)
	if err := db.Delete([]byte("k0")); !errors.Is(err, ErrFull) {
		t.Fatalf("got %v", err)
	}
	if fileSize(t, p) != before {
		t.Fatal("refused delete changed the file")
	}
}

func TestFileNeverExceedsTheCapRandomized(t *testing.T) {
	for seed := int64(1); seed <= 4; seed++ {
		rng := rand.New(rand.NewSource(seed))
		p := newPath(t)
		const limit = 150_000
		db := open(t, p, Options{MaxBytes: limit, SetMaxBytes: true, CheckpointBytes: 20_000})
		model := map[string]string{}
		for step := 0; step < 1500; step++ {
			var ops []Op
			for n := 1 + rng.Intn(4); n > 0; n-- {
				k := fmt.Sprintf("key%03d", rng.Intn(60))
				if rng.Intn(3) == 0 {
					ops = append(ops, Op{Kind: OpDelete, Key: []byte(k)})
				} else {
					ops = append(ops, Op{Kind: OpPut, Key: []byte(k), Value: make([]byte, rng.Intn(3000))})
				}
			}
			err := db.Batch(ops)
			switch {
			case err == nil:
				for _, op := range ops {
					if op.Kind == OpPut {
						model[string(op.Key)] = fmt.Sprint(len(op.Value))
					} else {
						delete(model, string(op.Key))
					}
				}
			case errors.Is(err, ErrFull):
				// definitely NOT applied: the model stays as it was
			default:
				t.Fatalf("seed %d step %d: %v", seed, step, err)
			}
			if sz := fileSize(t, p); sz > limit {
				t.Fatalf("seed %d step %d: file %d exceeds the cap %d", seed, step, sz, limit)
			}
			if step%300 == 299 {
				db.Close()
				db = open(t, p)
			}
		}
		if db.keyCount() != len(model) {
			t.Fatalf("seed %d: %d keys, model %d", seed, db.keyCount(), len(model))
		}
		for k, n := range model {
			v, ok, _ := db.Get([]byte(k))
			if !ok || fmt.Sprint(len(v)) != n {
				t.Fatalf("seed %d: key %s has %d bytes, model %s", seed, k, len(v), n)
			}
		}
		db.Close()
		if rep, _ := Check(p); !rep.OK {
			t.Fatalf("seed %d: %+v", seed, rep)
		}
	}
}
