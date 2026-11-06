package engine

// Read-only recovery (SPEC 7). Nothing here writes a byte of the data file.

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// scanResult is what a strict scan of the frames learned.
type scanResult struct {
	idx          *index
	logicalEnd   int64
	nextSeq      uint64
	frames       int
	tail         string // TailComplete | TailTorn | TailShortSentinel
	tailBytes    int64  // TORN: bytes after logicalEnd
	shortMissing int    // SHORT_SENTINEL: end_magic bytes missing at EOF
	// Why the scan stopped (nil when the file ended exactly on a frame boundary), and where.
	stopErr error
	stopOff int64
}

// scanImage scans frames strictly from DataStart: it applies each valid BATCH, validates
// and skips each KEYDIR, and stops at the first invalid frame (CP-R03). A frame is applied
// only after ALL of SPEC 7.2 checks 1 to 8 pass. A non-nil error means an I/O failure,
// not an invalid frame.
func scanImage(f *os.File, size int64, epoch uint64) (*scanResult, error) {
	res := &scanResult{idx: newIndex(), nextSeq: 1, tail: TailComplete}
	off := int64(DataStart)
	for {
		if off >= size {
			res.logicalEnd = off
			return res, nil // COMPLETE: the last frame ends exactly at EOF
		}
		if res.nextSeq == 0 { // sequence overflow is rejected
			res.stopErr = frameErr("seq", "sequence number overflow")
			break
		}
		info, err := ScanFrame(f, size, off, epoch, res.nextSeq)
		if err == nil {
			err = res.applyFrame(f, info)
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
func (r *scanResult) applyFrame(f *os.File, info FrameInfo) error {
	L := info.Header.PayloadLen
	pr := io.NewSectionReader(f, info.PayloadOff(), int64(L))
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
