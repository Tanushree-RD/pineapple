package engine

// Disk-full admission: append-or-rewrite (SPEC 8). max_bytes caps the physical size of each
// database image. For a transaction T with frame size F at logical_end E:
//
//	max_bytes == 0 or E + F <= max_bytes      -> APPEND   (the normal path)
//	otherwise, S = size bound of the image AFTER T, H = clamp(max/10, 64 KiB, max/2):
//	  T has a PUT:   S + H <= max_bytes       -> REWRITE  else ErrFull
//	  delete-only:   S     <= max_bytes       -> REWRITE  else ErrFull
//
// ErrFull is decided before any byte is written. REWRITE builds a compacted image of the
// post-T state in path.compact.tmp and swaps it in with the same replaceFile() as Compact.
// Only a buffered Apply can REWRITE; a streamed batch can only APPEND (SPEC 4.7).

type finalOp struct {
	put bool
	val []byte
}

// rewriteOrFull is called when the append does not fit under the cap.
func (db *DB) rewriteOrFull(t *txn, limit int64) error {
	if t.ops == nil { // ApplyStream: APPEND only
		return ErrFull
	}
	final := make(map[string]finalOp, len(t.ops))
	hasPut := false
	for _, op := range t.ops { // last op on a key wins
		if op.Kind == OpPut {
			final[string(op.Key)] = finalOp{put: true, val: op.Value}
			hasPut = true
		} else {
			final[string(op.Key)] = finalOp{}
		}
	}
	// Accounting of the post-T live set, without touching the index.
	rec, keyBytes, count := db.idx.liveRec, db.idx.liveKeyBytes, int64(len(db.idx.m))
	for k, f := range final {
		if old, ok := db.idx.m[k]; ok {
			rec -= recSize(len(k), old.len)
			keyBytes -= int64(len(k))
			count--
		}
		if f.put {
			rec += recSize(len(k), uint32(len(f.val)))
			keyBytes += int64(len(k))
			count++
		}
	}
	S := sizeUpperBound(rec, keyBytes, count)
	if hasPut {
		H := limit / 10 // near-cap headroom guard: no full rewrite for every small put
		if H < 64<<10 {
			H = 64 << 10
		}
		if H > limit/2 {
			H = limit / 2
		}
		if S+H > limit {
			return ErrFull
		}
	} else if S > limit { // delete-only: a rewrite needs no tombstone space (E5)
		return ErrFull
	}

	items := make([]liveItem, 0, len(db.idx.m)+len(final))
	for k, e := range db.idx.m {
		if _, touched := final[k]; !touched {
			items = append(items, liveItem{key: k, old: e})
		}
	}
	for k, f := range final {
		if f.put {
			items = append(items, liveItem{key: k, mem: f.val, hasMem: true})
		}
	}
	img, err := db.buildImage(items)
	if err != nil {
		return db.poison(err)
	}
	if err := db.replaceFile(img); err != nil { // success only after the dir fsync and the swap
		return db.poison(err)
	}
	if t.tag != 0 && db.opts.OnCommit != nil {
		db.opts.OnCommit(t.tag)
	}
	return nil
}
