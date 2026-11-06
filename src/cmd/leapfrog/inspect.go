package main

// `leapfrog inspect <db>`: decode a database file and show what is physically in it.
//
// This decoder is written from the on-disk format in docs/SPEC.md section 4 and does not use the
// engine's parser, so what it prints is an independent view of the bytes. It reads the file only;
// it never takes the lock and never writes.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"hash/crc64"
	"io"
	"os"
	"strings"

	"github.com/chethan-9817/pineapple/src/internal/tui"
)

const (
	slotSize   = 4096
	dataStart  = 8192
	hdrLen     = 40
	maxPayload = 4 + 10000*(7+1024+1048576)
	verifyMax  = 64 << 20 // do not read more than this per frame just to check its CRC64
)

var (
	castagnoli = crc32.MakeTable(crc32.Castagnoli)
	ecma       = crc64.MakeTable(crc64.ECMA)
)

type slot struct {
	valid                                     bool
	gen, epoch, ckptOff, ckptSeq, replay, cap uint64
}

type frame struct {
	off       int64
	typ       byte
	seq       uint64
	payload   uint64
	end       int64
	crcOK     bool
	endOK     bool
	verified  bool
	summary   string
	shortMiss int // end_magic bytes missing at EOF
}

type image struct {
	size      int64
	slots     [2]slot
	chosen    int
	frames    []frame
	tail      string
	tailBytes int64
	stop      string
}

func parseSlot(b []byte) slot {
	if len(b) < 68 || string(b[0:8]) != "LFROG-SB" || binary.LittleEndian.Uint32(b[8:12]) != 1 ||
		binary.LittleEndian.Uint32(b[64:68]) != crc32.Checksum(b[0:64], castagnoli) {
		return slot{}
	}
	le := binary.LittleEndian
	return slot{true, le.Uint64(b[16:24]), le.Uint64(b[24:32]), le.Uint64(b[32:40]), le.Uint64(b[40:48]), le.Uint64(b[48:56]), le.Uint64(b[56:64])}
}

// readImage decodes the file. It stops at the first invalid frame, exactly as recovery does.
func readImage(path string) (*image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	im := &image{size: st.Size(), tail: "COMPLETE"}
	for i := 0; i < 2; i++ {
		b := make([]byte, slotSize)
		if n, _ := f.ReadAt(b, int64(i)*slotSize); n >= 68 {
			im.slots[i] = parseSlot(b[:n])
		}
	}
	switch {
	case !im.slots[0].valid && !im.slots[1].valid:
		return nil, fmt.Errorf("no valid superblock slot: this is not a LEAPFROG-R database (or it is damaged beyond the crash model)")
	case !im.slots[0].valid, im.slots[1].valid && im.slots[1].gen > im.slots[0].gen:
		im.chosen = 1
	}
	epoch := im.slots[im.chosen].epoch

	off, seq := int64(dataStart), uint64(1)
	for off < im.size {
		fr, reason := readFrame(f, im.size, off, epoch, seq)
		if fr == nil {
			im.tail, im.tailBytes, im.stop = "TORN", im.size-off, reason
			return im, nil
		}
		im.frames = append(im.frames, *fr)
		if fr.shortMiss > 0 {
			im.tail, im.tailBytes = "SHORT_SENTINEL", int64(fr.shortMiss)
			return im, nil
		}
		off, seq = fr.end, seq+1
	}
	return im, nil
}

func readFrame(f *os.File, size, off int64, epoch, wantSeq uint64) (*frame, string) {
	if size-off < hdrLen {
		return nil, fmt.Sprintf("only %d bytes left, a header needs 40", size-off)
	}
	h := make([]byte, hdrLen)
	if _, err := f.ReadAt(h, off); err != nil && err != io.EOF {
		return nil, err.Error()
	}
	le := binary.LittleEndian
	switch {
	case string(h[0:4]) != "LFR1":
		return nil, "bad magic"
	case h[4] != 1 && h[4] != 2, h[5] != 1, h[6] != 0, h[7] != 0, le.Uint32(h[36:40]) != 0:
		return nil, "bad type, version or reserved bytes"
	case le.Uint32(h[32:36]) != crc32.Checksum(h[0:32], castagnoli):
		return nil, "header CRC mismatch"
	case le.Uint64(h[8:16]) != epoch:
		return nil, "epoch mismatch (a frame of another image)"
	case le.Uint64(h[16:24]) != wantSeq:
		return nil, fmt.Sprintf("sequence %d, expected %d", le.Uint64(h[16:24]), wantSeq)
	}
	l := le.Uint64(h[24:32])
	remaining := uint64(size - off)
	if l > maxPayload || remaining < 48+l {
		return nil, "frame is cut short (payload or CRC64 missing)"
	}
	fr := &frame{off: off, typ: h[4], seq: wantSeq, payload: l, end: off + int64(56+l)}
	// Sentinel.
	endAt := off + int64(48+l)
	have := size - endAt
	if have > 8 {
		have = 8
	}
	tail := make([]byte, have)
	if have > 0 {
		f.ReadAt(tail, endAt)
	}
	if remaining >= 56+l {
		fr.endOK = string(tail) == "LF-END!!"
		if !fr.endOK {
			return nil, "end sentinel mismatch"
		}
	} else {
		if !bytes.HasPrefix([]byte("LF-END!!"), tail) {
			return nil, "partial end sentinel is not a prefix of LF-END!!"
		}
		fr.shortMiss = 8 - int(have)
	}
	// CRC64 over header + payload, when it is not too large to read.
	if l+hdrLen <= verifyMax {
		body := make([]byte, hdrLen+l)
		if _, err := f.ReadAt(body, off); err != nil && err != io.EOF {
			return nil, err.Error()
		}
		var crc [8]byte
		f.ReadAt(crc[:], off+int64(hdrLen+l))
		if le.Uint64(crc[:]) != crc64.Checksum(body, ecma) {
			return nil, "frame CRC64 mismatch"
		}
		fr.crcOK, fr.verified = true, true
		fr.summary = summarize(h[4], body[hdrLen:])
	} else {
		fr.summary = "large frame (CRC64 not re-checked here)"
	}
	return fr, ""
}

func summarize(typ byte, p []byte) string {
	le := binary.LittleEndian
	if typ == 2 {
		if len(p) >= 16 {
			return fmt.Sprintf("checkpoint: %d keys indexed", le.Uint64(p[8:16]))
		}
		return "checkpoint"
	}
	if len(p) < 4 {
		return "?"
	}
	n := le.Uint32(p[0:4])
	if len(p) >= 4+7 {
		kind, kl, vl := p[4], int(le.Uint16(p[5:7])), le.Uint32(p[7:11])
		if len(p) >= 11+kl {
			key := p[11 : 11+kl]
			what := "put"
			if kind == 2 {
				what = "del"
			}
			s := fmt.Sprintf("%s %s", what, esc(key))
			if kind == 1 {
				s += fmt.Sprintf(" (%s)", tui.Bytes(int64(vl)))
			}
			if n > 1 {
				s = fmt.Sprintf("%d ops, first: %s", n, s)
			}
			return s
		}
	}
	return fmt.Sprintf("%d ops", n)
}

func esc(b []byte) string {
	ok := len(b) > 0 && string(b) != "NOTFOUND" && !bytes.HasPrefix(b, []byte("hex:"))
	for _, c := range b {
		if c < 0x20 || c > 0x7e || c == '\\' {
			ok = false
		}
	}
	if ok {
		return string(b)
	}
	return "hex:" + hex.EncodeToString(b)
}

func typeName(t byte) string {
	if t == 2 {
		return "KEYDIR"
	}
	return "BATCH"
}

// printImage renders the decoded file as boxes.
func printImage(w io.Writer, path string, im *image, verbose bool) {
	width := tui.Width()
	fmt.Fprintln(w)
	var sb []string
	sb = append(sb, fmt.Sprintf("%s  %s", tui.Bold(path), tui.Gray(tui.Bytes(im.size)+" on disk")))
	for _, l := range tui.Box("file", sb, width, tui.Blue) {
		fmt.Fprintln(w, l)
	}

	var sl []string
	for i, s := range im.slots {
		name := string(rune('A' + i))
		if !s.valid {
			sl = append(sl, fmt.Sprintf("slot %s  %s %s", name, tui.Fail(), tui.Gray("empty or invalid (fails magic/CRC32C)")))
			continue
		}
		ck := "no checkpoint"
		if s.ckptOff != 0 {
			ck = fmt.Sprintf("checkpoint @%d (seq %d), replay from %d", s.ckptOff, s.ckptSeq, s.replay)
		}
		capS := "no size cap"
		if s.cap != 0 {
			capS = "cap " + tui.Bytes(int64(s.cap))
		}
		mark := ""
		if i == im.chosen {
			mark = "  " + tui.Cyan("◀ ACTIVE")
		}
		sl = append(sl, fmt.Sprintf("slot %s  %s generation %d%s", name, tui.OK(), s.gen, mark))
		sl = append(sl, tui.Gray(fmt.Sprintf("        epoch 0x%x · %s · %s", s.epoch, ck, capS)))
	}
	for _, l := range tui.Box("superblocks  (bytes 0 to 8191)", sl, width, tui.Magenta) {
		fmt.Fprintln(w, l)
	}

	var fl []string
	show := func(i int) {
		f := im.frames[i]
		status := tui.OK()
		if !f.verified {
			status = tui.Warn()
		}
		if f.shortMiss > 0 {
			status = tui.Warn() + tui.Yellow(fmt.Sprintf(" -%d B end marker, still valid", f.shortMiss))
		}
		fl = append(fl, fmt.Sprintf("%s %s %s seq %-4d L=%-7d %s %s",
			tui.Gray(fmt.Sprintf("#%-3d", i+1)), tui.Gray(fmt.Sprintf("@%-8d", f.off)), tui.Cyan(tui.PadRight(typeName(f.typ), 6)), f.seq, f.payload, tui.Trunc(f.summary, 30), status))
	}
	switch n := len(im.frames); {
	case n == 0:
		fl = append(fl, tui.Gray("no frames yet: a freshly created database"))
	case n <= 14:
		for i := range im.frames {
			show(i)
		}
	default:
		for i := 0; i < 8; i++ {
			show(i)
		}
		fl = append(fl, tui.Gray(fmt.Sprintf("      … %d more frames …", n-14)))
		for i := n - 6; i < n; i++ {
			show(i)
		}
	}
	for _, l := range tui.Box(fmt.Sprintf("frames  (%d, from byte 8192, ✔ = CRC64 verified)", len(im.frames)), fl, width, tui.Cyan) {
		fmt.Fprintln(w, l)
	}

	var tail []string
	switch im.tail {
	case "COMPLETE":
		tail = []string{tui.OK() + " " + tui.Bold("COMPLETE") + tui.Gray("  the last frame ends exactly at the end of the file")}
	case "TORN":
		tail = []string{tui.Warn() + " " + tui.Bold("TORN") + tui.Gray(fmt.Sprintf("  %d bytes after the last valid frame are ignored", im.tailBytes)),
			tui.Gray("   why: " + im.stop + ". The next write truncates them before appending.")}
	default:
		tail = []string{tui.Warn() + " " + tui.Bold("SHORT_SENTINEL") + tui.Gray(fmt.Sprintf("  %d byte(s) of the end marker are missing", im.tailBytes)),
			tui.Gray("   header, payload and CRC64 are intact, so the frame counts (an ACKed write survives, F4).")}
	}
	for _, l := range tui.Box("tail", tail, width, tui.Yellow) {
		fmt.Fprintln(w, l)
	}
	if verbose && len(im.frames) > 0 {
		printAnatomy(w, path, im.frames[0], width)
	}
	fmt.Fprintln(w)
}

// printAnatomy labels every field of the first frame, byte by byte.
func printAnatomy(w io.Writer, path string, fr frame, width int) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	b := make([]byte, fr.end-fr.off)
	if _, err := f.ReadAt(b, fr.off); err != nil && err != io.EOF {
		return
	}
	le := binary.LittleEndian
	type field struct {
		off, n int
		name   string
		note   string
	}
	L := int(fr.payload)
	fields := []field{
		{0, 4, "magic", "\"LFR1\""},
		{4, 1, "type", typeName(fr.typ)},
		{5, 1, "version", "1"},
		{6, 2, "reserved", "0"},
		{8, 8, "epoch", "must equal the superblock's"},
		{16, 8, "seq", fmt.Sprint(le.Uint64(b[16:24]))},
		{24, 8, "payload_len", fmt.Sprint(L)},
		{32, 4, "header_crc", "CRC32C of 0..31, checked before L"},
		{36, 4, "reserved", "0"},
		{40, L, "payload", "the operations"},
		{40 + L, 8, "frame_crc", "CRC64: all-or-nothing"},
		{48 + L, 8, "end_magic", "\"LF-END!!\""},
	}
	var lines []string
	for _, fd := range fields {
		if fd.off+fd.n > len(b) {
			continue
		}
		raw := b[fd.off : fd.off+fd.n]
		n := len(raw)
		if n > 8 {
			n = 8
		}
		parts := make([]string, 0, n)
		for _, c := range raw[:n] {
			parts = append(parts, fmt.Sprintf("%02x", c))
		}
		s := strings.Join(parts, " ")
		if len(raw) > 8 {
			s += " …"
		}
		lines = append(lines, fmt.Sprintf("%s  %-23s %s %s", tui.Gray(fmt.Sprintf("+%-5d", fd.off)), tui.Cyan(s), tui.Bold(fmt.Sprintf("%-11s", fd.name)), tui.Gray(fd.note)))
	}
	for _, l := range tui.Box("anatomy of frame #1", lines, width, tui.Green) {
		fmt.Fprintln(w, l)
	}
}
