package engine

// Read-only recovery (SPEC 7). Nothing here writes a byte of the data file.

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// scanResult is what recovery learned.
type scanResult struct {
	idx          *index
	logicalEnd   int64
	nextSeq      uint64
	frames       int
	tail         string // TailComplete | TailTorn | TailShortSentinel
	tailBytes    int64  // TORN: bytes after logicalEnd
	shortMissing int    // SHORT_SENTINEL: end_magic bytes missing at EOF
	fromCkpt     bool   // the index started from a valid checkpoint
	// Why the scan stopped (nil when the file ended exactly on a frame boundary), and where.
	stopErr error
	stopOff int64
}

// windowReader serves sequential scans from a 1 MiB read-ahead window, so recovering many
// small frames costs about one system call per MiB instead of several per frame.
// It is NOT safe for concurrent use; only recovery and Check use it.
type windowReader struct {
	f   io.ReaderAt
	buf []byte
	off int64
	n   int
}

const windowSize = 1 << 20

func newWindowReader(f io.ReaderAt) *windowReader {
	return &windowReader{f: f, buf: make([]byte, windowSize)}
}

func (w *windowReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) >= len(w.buf) {
		return w.f.ReadAt(p, off) // big read: bypass the window
	}
	if !(off >= w.off && off+int64(len(p)) <= w.off+int64(w.n)) {
		n, err := w.f.ReadAt(w.buf, off)
		if err != nil && err != io.EOF {
			return 0, err
		}
		w.off, w.n = off, n
	}
	if off < w.off || off+int64(len(p)) > w.off+int64(w.n) {
		// Past the end of the file: serve what exists.
		if off >= w.off && off < w.off+int64(w.n) {
			n := copy(p, w.buf[off-w.off:w.n])
			return n, io.EOF
		}
		return 0, io.EOF
	}
	return copy(p, w.buf[off-w.off:]), nil
}

// recoverImage is SPEC 7.1 steps 5 to 7: try the checkpoint, else (or after it) scan.
func recoverImage(f *os.File, size int64, sb Superblock) (*scanResult, error) {
	w := newWindowReader(f)
	if sb.CkptOff != 0 {
		// CP-R02: checkpoint load. An invalid checkpoint is not an error: fall back to a
		// full scan, which gives the same index, only slower.
		res, ok, err := loadCheckpoint(w, size, sb)
		if err != nil {
			return nil, err
		}
		if ok {
			return scanFrom(w, size, sb.Epoch, res, int64(sb.ReplayStart))
		}
	}
	return scanFrom(w, size, sb.Epoch, &scanResult{idx: newIndex(), nextSeq: 1}, DataStart)
}

// loadCheckpoint validates the checkpoint named by the superblock (SPEC 7.1 step 5): a
// KEYDIR frame of the slot's epoch whose seq, payload checkpoint_seq and ckpt_seq all agree,
// whose entries are valid, and whose end equals replay_start. ok=false means "ignore it".
func loadCheckpoint(r io.ReaderAt, size int64, sb Superblock) (res *scanResult, ok bool, err error) {
	if sb.CkptSeq == 0 || sb.CkptOff < DataStart || int64(sb.CkptOff) >= size {
		return nil, false, nil
	}
	info, err := ScanFrame(r, size, int64(sb.CkptOff), sb.Epoch, sb.CkptSeq)
	if err != nil {
		var fe *FrameError
		if errors.As(err, &fe) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if info.Header.Type != FrameTypeKeydir || info.ShortSentinel != 0 || uint64(info.End) != sb.ReplayStart {
		return nil, false, nil
	}
	L := info.Header.PayloadLen
	ents, err := ParseKeydir(io.NewSectionReader(r, info.PayloadOff(), int64(L)), L, sb.CkptSeq, sb.CkptOff)
	if err != nil {
		var fe *FrameError
		if errors.As(err, &fe) {
			return nil, false, nil
		}
		return nil, false, err
	}
	idx := newIndexSize(len(ents))
	for _, e := range ents {
		idx.set(e.Key, entry{off: e.ValueOff, len: e.ValueLen})
	}
	return &scanResult{idx: idx, nextSeq: sb.CkptSeq + 1, fromCkpt: true}, true, nil
}

// scanImage is a full strict scan from DataStart (used by Check).
func scanImage(f *os.File, size int64, epoch uint64) (*scanResult, error) {
	return scanFrom(newWindowReader(f), size, epoch, &scanResult{idx: newIndex(), nextSeq: 1}, DataStart)
}

// scanFrom scans frames strictly starting at off: it applies each valid BATCH, validates and
// skips each KEYDIR, and stops at the first invalid frame (CP-R03). A frame is applied only
// after ALL of SPEC 7.2 checks 1 to 8 pass. A non-nil error means an I/O failure, not an
// invalid frame.
func scanFrom(r io.ReaderAt, size int64, epoch uint64, res *scanResult, off int64) (*scanResult, error) {
	res.tail = TailComplete
	for {
		if off >= size {
			res.logicalEnd = off
			return res, nil // COMPLETE: the last frame ends exactly at EOF
		}
		if res.nextSeq == 0 { // sequence overflow is rejected
			res.stopErr = frameErr("seq", "sequence number overflow")
			break
		}
		info, err := ScanFrame(r, size, off, epoch, res.nextSeq)
		if err == nil {
			err = res.applyFrame(r, info)
		}
		if err != nil {
			var fe *FrameError
			if !errors.As(err, &fe) {
				return nil, err // a real I/O error, not an invalid frame
			}
			res.stopErr = err
			break
		}
		res.frames++
		res.nextSeq++
		off = info.End
		if info.ShortSentinel > 0 { // SHORT_SENTINEL: frame accepted, it ends past EOF
			res.tail = TailShortSentinel
			res.shortMissing = info.ShortSentinel
			res.logicalEnd = info.End
			return res, nil
		}
	}
	res.stopOff = off
	res.logicalEnd = off
	res.tail = TailTorn
	res.tailBytes = size - off
	return res, nil
}

// applyFrame runs check 8 (exact payload parse) and, only if it passes, applies the frame.
func (r *scanResult) applyFrame(src io.ReaderAt, info FrameInfo) error {
	L := info.Header.PayloadLen
	pr := io.NewSectionReader(src, info.PayloadOff(), int64(L))
	switch info.Header.Type {
	case FrameTypeBatch:
		ents, err := ParseBatch(pr, L)
		if err != nil {
			return err
		}
		r.idx.applyBatch(info.Off, ents) // one frame, applied whole
	case FrameTypeKeydir:
		// Validated and SKIPPED during a scan: a KEYDIR never changes the index (SPEC 4.5).
		if _, err := ParseKeydir(pr, L, info.Header.Seq, uint64(info.Off)); err != nil {
			return err
		}
	}
	return nil
}

// readSuperblocks reads both slots and picks the current one (CP-R01).
func readSuperblocks(f *os.File, size int64) (slot int, sb Superblock, err error) {
	if size < DataStart {
		return 0, Superblock{}, fmt.Errorf("%w: file is %d bytes, shorter than the 8192-byte superblock area", ErrCorrupt, size)
	}
	var hdr [DataStart]byte
	if _, err := f.ReadAt(hdr[:], 0); err != nil {
		return 0, Superblock{}, err
	}
	return ChooseSuperblock(hdr[:SuperblockSlotSize], hdr[SuperblockSlotSize:])
}
