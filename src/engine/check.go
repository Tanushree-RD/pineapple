package engine

// Check verifies a database file read-only (SPEC 13).

import (
	"errors"
	"fmt"
	"os"
)

// Check takes path.lock (ErrLocked if busy), reads both superblocks, then runs the same
// strict scan recovery uses and reports exactly where and why it stopped.
//
// What is NOT corruption: bytes after the last valid frame that are a torn prefix of the
// one write that was in flight at a kill, i.e. a frame cut short (header incomplete, or
// a valid header whose payload and CRC64 run past EOF). Those are reported as INFO.
// What IS corruption: a frame that is completely present but wrong (bad CRC64, bad
// sentinel, bad payload, wrong epoch or seq), because a process kill cannot produce one.
func Check(path string) (Report, error) {
	lockF, err := lockFile(path + ".lock")
	if err != nil {
		return Report{}, err
	}
	defer unlockFile(lockF)

	f, err := os.Open(path)
	if err != nil {
		return Report{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Report{}, err
	}
	rep := Report{}
	if fi.Size() < DataStart {
		rep.Reason = fmt.Sprintf("file is %d bytes, shorter than the 8192-byte superblock area", fi.Size())
		return rep, nil
	}
	var hdr [DataStart]byte
	if _, err := f.ReadAt(hdr[:], 0); err != nil {
		return Report{}, err
	}
	names := [2]string{"A", "B"}
	for i := 0; i < 2; i++ {
		s, e := ParseSuperblock(hdr[i*SuperblockSlotSize : (i+1)*SuperblockSlotSize])
		if e != nil {
			rep.Info = append(rep.Info, fmt.Sprintf("slot %s invalid (%v)", names[i], e))
		} else {
			rep.Info = append(rep.Info, fmt.Sprintf("slot %s valid generation=%d epoch=%#x", names[i], s.Generation, s.Epoch))
		}
	}
	idx, sb, err := ChooseSuperblock(hdr[:SuperblockSlotSize], hdr[SuperblockSlotSize:])
	if err != nil {
		rep.Reason = err.Error()
		return rep, nil
	}
	rep.Info = append(rep.Info, fmt.Sprintf("using slot %s", names[idx]))

	res, err := scanImage(f, fi.Size(), sb.Epoch)
	if err != nil {
		return Report{}, err
	}
	rep.Keys = len(res.idx.m)
	rep.Info = append(rep.Info,
		fmt.Sprintf("frames=%d", res.frames),
		fmt.Sprintf("max_bytes=%d", sb.MaxBytes),
		fmt.Sprintf("file_size=%d", fi.Size()))

	switch res.tail {
	case TailTorn:
		if why := corruptionAt(f, fi.Size(), res, sb.Epoch); why != "" {
			rep.Reason = why
			return rep, nil
		}
		rep.Info = append(rep.Info, fmt.Sprintf("tail=TORN bytes=%d (unacknowledged)", res.tailBytes))
	case TailShortSentinel:
		rep.Info = append(rep.Info, fmt.Sprintf("tail=SHORT_SENTINEL missing=%d (frame accepted)", res.shortMissing))
	default:
		rep.Info = append(rep.Info, "tail=COMPLETE")
	}
	rep.OK = true
	return rep, nil
}

// corruptionAt decides whether the invalid bytes at the scan stop are a legal torn tail.
// It returns "" for a legal tail, or a precise CORRUPT reason.
func corruptionAt(f *os.File, size int64, res *scanResult, epoch uint64) string {
	var fe *FrameError
	if !errors.As(res.stopErr, &fe) {
		return ""
	}
	where := fmt.Sprintf("frame seq=%d at offset %d: %s: %s", res.nextSeq, res.stopOff, fe.Check, fe.Detail)
	switch fe.Check {
	case "truncated":
		return "" // fewer than 40 bytes left: a header cut short
	case "length":
		// Legal only if the header itself is sound and merely runs past EOF.
		var h [FrameHeaderLen]byte
		if _, err := f.ReadAt(h[:], res.stopOff); err != nil {
			return where
		}
		hd, err := DecodeHeader(h[:])
		if err == nil && hd.Epoch == epoch && hd.Seq == res.nextSeq && hd.PayloadLen <= MaxPayload {
			return ""
		}
		return where
	}
	return where
}
