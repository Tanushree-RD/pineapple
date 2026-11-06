package engine

// Frame header, checks and streaming writer (SPEC 4.3, 4.7, 7.2).
//
//	0 magic "LFR1" (4)  4 type (1)  5 version (1)  6 reserved (2)
//	8 epoch (8)         16 seq (8)   24 payload_len L (8)
//	32 header_crc = CRC32C(bytes 0..31) (4)  36 reserved (4)
//	40 payload (L)  40+L frame_crc = CRC64(bytes 0..39+L) (8)  48+L end_magic (8)

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"
)

type FrameHeader struct {
	Type       uint8
	Epoch      uint64
	Seq        uint64
	PayloadLen uint64
}

// EncodeHeader returns the 40-byte frame header.
func EncodeHeader(h FrameHeader) [FrameHeaderLen]byte {
	var b [FrameHeaderLen]byte
	copy(b[0:4], FrameMagic[:])
	b[4] = h.Type
	b[5] = FrameVersion
	binary.LittleEndian.PutUint64(b[8:], h.Epoch)
	binary.LittleEndian.PutUint64(b[16:], h.Seq)
	binary.LittleEndian.PutUint64(b[24:], h.PayloadLen)
	binary.LittleEndian.PutUint32(b[32:], crc32.Checksum(b[0:32], Crc32cTable))
	return b
}

// DecodeHeader runs SPEC 7.2 checks 1 and 2: magic, type, version, reserved fields,
// then header_crc. It does not trust PayloadLen; the caller bounds it (check 5).
func DecodeHeader(b []byte) (FrameHeader, error) {
	if len(b) < FrameHeaderLen {
		return FrameHeader{}, frameErr("truncated", "header needs %d bytes, have %d", FrameHeaderLen, len(b))
	}
	if [4]byte(b[0:4]) != FrameMagic {
		return FrameHeader{}, frameErr("magic", "got % x", b[0:4])
	}
	if t := b[4]; t != FrameTypeBatch && t != FrameTypeKeydir {
		return FrameHeader{}, frameErr("type", "unknown frame type %d", t)
	}
	if b[5] != FrameVersion {
		return FrameHeader{}, frameErr("version", "frame version %d", b[5])
	}
	if b[6] != 0 || b[7] != 0 || binary.LittleEndian.Uint32(b[36:]) != 0 {
		return FrameHeader{}, frameErr("reserved", "reserved header bytes are not zero")
	}
	if crc32.Checksum(b[0:32], Crc32cTable) != binary.LittleEndian.Uint32(b[32:]) {
		return FrameHeader{}, frameErr("header_crc", "mismatch")
	}
	return FrameHeader{
		Type:       b[4],
		Epoch:      binary.LittleEndian.Uint64(b[8:]),
		Seq:        binary.LittleEndian.Uint64(b[16:]),
		PayloadLen: binary.LittleEndian.Uint64(b[24:]),
	}, nil
}

// FrameInfo describes a frame that passed ScanFrame.
type FrameInfo struct {
	Header FrameHeader
	Off    int64
	End    int64 // Off + 56 + L; beyond the file size when ShortSentinel > 0
	// ShortSentinel is the number of end_magic bytes missing at physical EOF (0..7).
	// 0 means the frame is complete. A non-zero value is the SHORT_SENTINEL tail (7.3).
	ShortSentinel int
}

// PayloadOff is the absolute offset of the payload.
func (f FrameInfo) PayloadOff() int64 { return f.Off + FrameHeaderLen }

const scanChunk = 1 << 20

// ScanFrame runs SPEC 7.2 checks 1 to 7 on the frame at off. size is the file size, so
// R = size - off. The payload is streamed through the CRC64 in 1 MiB steps and is never
// held in memory; nothing is allocated from L before check 5 passes. The caller runs
// check 8 (exact payload parse) with ParseBatch or ParseKeydir on PayloadOff.
func ScanFrame(r io.ReaderAt, size, off int64, epoch, expectSeq uint64) (FrameInfo, error) {
	R := size - off
	if R < FrameHeaderLen {
		return FrameInfo{}, frameErr("truncated", "%d bytes left, header needs %d", R, FrameHeaderLen)
	}
	var hb [FrameHeaderLen]byte
	if _, err := r.ReadAt(hb[:], off); err != nil {
		return FrameInfo{}, fmt.Errorf("read frame header at %d: %w", off, err)
	}
	h, err := DecodeHeader(hb[:])
	if err != nil {
		return FrameInfo{}, err
	}
	if h.Epoch != epoch {
		return FrameInfo{}, frameErr("epoch", "frame epoch %#x, image epoch %#x", h.Epoch, epoch)
	}
	if h.Seq != expectSeq {
		return FrameInfo{}, frameErr("seq", "frame seq %d, expected %d", h.Seq, expectSeq)
	}
	// Check 5: L is bounded, and header + payload + CRC64 fit, computed without overflow.
	if h.PayloadLen > MaxPayload {
		return FrameInfo{}, frameErr("length", "payload_len %d exceeds MAX_PAYLOAD", h.PayloadLen)
	}
	if R < 48 || h.PayloadLen > uint64(R-48) {
		return FrameInfo{}, frameErr("length", "payload_len %d does not fit: %d bytes left", h.PayloadLen, R)
	}
	L := int64(h.PayloadLen)

	// Check 6: CRC64 over bytes 0..39+L.
	crc := crc64.New(Crc64Table)
	crc.Write(hb[:])
	buf := make([]byte, scanChunk)
	for done := int64(0); done < L; {
		n := int64(len(buf))
		if L-done < n {
			n = L - done
		}
		if _, err := r.ReadAt(buf[:n], off+FrameHeaderLen+done); err != nil {
			return FrameInfo{}, fmt.Errorf("read payload at %d: %w", off+FrameHeaderLen+done, err)
		}
		crc.Write(buf[:n])
		done += n
	}
	var tail [FrameTrailerLen]byte
	have := R - (FrameHeaderLen + L) // bytes at and after frame_crc, at least 8
	if have > FrameTrailerLen {
		have = FrameTrailerLen
	}
	if _, err := r.ReadAt(tail[:have], off+FrameHeaderLen+L); err != nil {
		return FrameInfo{}, fmt.Errorf("read frame trailer: %w", err)
	}
	if binary.LittleEndian.Uint64(tail[0:8]) != crc.Sum64() {
		return FrameInfo{}, frameErr("frame_crc", "mismatch")
	}

	// Check 7: sentinel. Complete: all 8 bytes. Otherwise the frame ends at EOF and the
	// 0..7 bytes present must be the matching prefix of LF-END!! (SHORT_SENTINEL).
	info := FrameInfo{Header: h, Off: off, End: off + FrameOverhead + L}
	present := int(have) - 8 // sentinel bytes actually in the file
	if present >= 8 {
		if [8]byte(tail[8:16]) != EndMagic {
			return FrameInfo{}, frameErr("sentinel", "end_magic is % x", tail[8:16])
		}
		return info, nil
	}
	for i := 0; i < present; i++ {
		if tail[8+i] != EndMagic[i] {
			return FrameInfo{}, frameErr("sentinel", "partial end_magic byte %d is %#x", i, tail[8+i])
		}
	}
	info.ShortSentinel = 8 - present
	return info, nil
}

// FrameWriter streams one frame: header, payload bytes in any number of Write calls,
// then the CRC64 and sentinel on Close. It keeps an incremental CRC64 and issues
// writes of at most WriteChunk bytes, so a frame is never held in memory (SPEC 4.7).
type FrameWriter struct {
	bw        *bufio.Writer
	crc       hash.Hash64
	remaining uint64
	err       error
	closed    bool
}

// NewFrameWriter writes the header for a payload of exactly payloadLen bytes.
func NewFrameWriter(w io.Writer, typ uint8, epoch, seq, payloadLen uint64) (*FrameWriter, error) {
	if payloadLen > MaxPayload {
		return nil, fmt.Errorf("%w: payload_len %d exceeds MAX_PAYLOAD", ErrInvalid, payloadLen)
	}
	if typ != FrameTypeBatch && typ != FrameTypeKeydir {
		return nil, fmt.Errorf("%w: frame type %d", ErrInvalid, typ)
	}
	hdr := EncodeHeader(FrameHeader{Type: typ, Epoch: epoch, Seq: seq, PayloadLen: payloadLen})
	f := &FrameWriter{
		bw:        bufio.NewWriterSize(w, 1<<20),
		crc:       crc64.New(Crc64Table),
		remaining: payloadLen,
	}
	f.crc.Write(hdr[:])
	if _, err := f.bw.Write(hdr[:]); err != nil {
		f.err = err
		return nil, err
	}
	return f, nil
}

// Write appends payload bytes. Writing more than the announced payload_len is an error.
func (f *FrameWriter) Write(p []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	if uint64(len(p)) > f.remaining {
		f.err = fmt.Errorf("%w: payload exceeds announced length", ErrInvalid)
		return 0, f.err
	}
	written := 0
	for len(p) > 0 {
		n := len(p)
		if n > WriteChunk {
			n = WriteChunk
		}
		f.crc.Write(p[:n])
		m, err := f.bw.Write(p[:n])
		written += m
		f.remaining -= uint64(m)
		if err != nil {
			f.err = err
			return written, err
		}
		p = p[n:]
	}
	return written, nil
}

// Close checks that exactly payload_len bytes were written, then writes frame_crc and
// end_magic and flushes. After Close returns nil the whole frame has been handed to w.
func (f *FrameWriter) Close() error {
	if f.err != nil {
		return f.err
	}
	if f.closed {
		return nil
	}
	if f.remaining != 0 {
		f.err = fmt.Errorf("%w: payload short by %d bytes", ErrInvalid, f.remaining)
		return f.err
	}
	var tr [FrameTrailerLen]byte
	binary.LittleEndian.PutUint64(tr[0:8], f.crc.Sum64())
	copy(tr[8:16], EndMagic[:])
	if _, err := f.bw.Write(tr[:]); err != nil {
		f.err = err
		return err
	}
	if err := f.bw.Flush(); err != nil {
		f.err = err
		return err
	}
	f.closed = true
	return nil
}
