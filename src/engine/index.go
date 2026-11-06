package engine

// The in-RAM index (SPEC 4.6) with live-set accounting (SPEC 8.2).

type entry struct {
	off uint64 // ABSOLUTE file offset of the value bytes
	len uint32
}

type index struct {
	m            map[string]entry
	liveRec      int64 // sum of (7 + key_len + value_len) over live keys
	liveKeyBytes int64 // sum of key_len over live keys
}

func newIndex() *index { return newIndexSize(0) }

func newIndexSize(n int) *index { return &index{m: make(map[string]entry, n)} }

func recSize(keyLen int, valLen uint32) int64 {
	return batchOpHeaderLen + int64(keyLen) + int64(valLen)
}

func (ix *index) set(key []byte, e entry) {
	k := string(key)
	if old, ok := ix.m[k]; ok {
		ix.liveRec -= recSize(len(k), old.len)
		ix.liveKeyBytes -= int64(len(k))
	}
	ix.m[k] = e
	ix.liveRec += recSize(len(k), e.len)
	ix.liveKeyBytes += int64(len(k))
}

func (ix *index) remove(key []byte) {
	k := string(key)
	if old, ok := ix.m[k]; ok {
		ix.liveRec -= recSize(len(k), old.len)
		ix.liveKeyBytes -= int64(len(k))
		delete(ix.m, k)
	}
}

// applyBatch applies a frame's ops in order (last op on a key wins). frameOff is the
// frame's start; a value lives at frameOff + 40 + ValueOff.
func (ix *index) applyBatch(frameOff int64, ents []BatchEntry) {
	for _, e := range ents {
		if e.Kind == OpPut {
			ix.set(e.Key, entry{off: uint64(frameOff) + FrameHeaderLen + e.ValueOff, len: e.ValueLen})
		} else {
			ix.remove(e.Key)
		}
	}
}
