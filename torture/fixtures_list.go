package main

// The recovery matrix of SPEC 15.8, plus the neighbours that SPEC 7 and 16 imply (F4 then write,
// stale bytes behind the tail, persisted cap, no valid superblock).

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// lastFrame returns the last frame of a freshly built database.
func lastFrame(im *Image) FrameInfo { return im.Frames[len(im.Frames)-1] }

// threeBase builds a=1, b=2, c=3 (one frame each) and decodes it.
func (e *fxEnv) threeBase(dir string) (db string, im *Image, data []byte, err error) {
	db = filepath.Join(dir, "base.db")
	if err = e.threePuts(db); err != nil {
		return
	}
	im, data, err = readImage(db)
	if err == nil && (len(im.Frames) != 3 || im.Tail != "COMPLETE") {
		err = fmt.Errorf("base database should have 3 complete frames, HYDRA's reader sees %d (%s)", len(im.Frames), im.Tail)
	}
	return
}

// cutFixture builds a/b/c, truncates the file to cut(last frame) bytes and expects want.
func cutFixture(name string, cut func(last FrameInfo) int64, want func() *fxWant) fixture {
	return fixture{name: name, run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		_, im, data, err := e.threeBase(dir)
		if err != nil {
			return nil, "", err
		}
		at := cut(lastFrame(im))
		if at <= 0 || at >= int64(len(data)) {
			return nil, "", fmt.Errorf("cut offset %d is outside the file (%d bytes)", at, len(data))
		}
		db, err := writeVariant(dir, "db", data[:at])
		return want(), db, err
	}}
}

func fixtureList() []fixture {
	torn := func() *fxWant {
		return &fxWant{keys: keysOf("a", "1", "b", "2"), tail: "TORN", check: "OK", infoHas: []string{"tail=TORN"}}
	}
	var fx []fixture

	fx = append(fx, fixture{name: "clean-empty", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		db := filepath.Join(dir, "db")
		return &fxWant{keys: keysOf(), tail: "COMPLETE", check: "OK", infoHas: []string{"frames=0"}}, db, e.kv("open", db)
	}})

	fx = append(fx, fixture{name: "clean-many", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		db, _, _, err := e.threeBase(dir)
		return &fxWant{keys: keysOf("a", "1", "b", "2", "c", "3"), tail: "COMPLETE", check: "OK", infoHas: []string{"frames=3"}}, db, err
	}})

	// A cut anywhere before the frame is whole leaves a TORN tail and the frame absent.
	fx = append(fx,
		cutFixture("torn-header", func(l FrameInfo) int64 { return l.Off + 20 }, torn),
		cutFixture("torn-payload", func(l FrameInfo) int64 { return l.Off + frameHdrLen + int64(l.PayloadLen)/2 }, torn),
		cutFixture("missing-crc", func(l FrameInfo) int64 { return l.Off + frameHdrLen + int64(l.PayloadLen) }, torn),
		cutFixture("torn-in-frame-crc", func(l FrameInfo) int64 { return l.Off + frameHdrLen + int64(l.PayloadLen) + 4 }, torn),
	)

	// F4: cutting 1 to 8 bytes of end_magic keeps the frame, because nothing else is missing.
	for cut := int64(1); cut <= 8; cut++ {
		cut := cut
		fx = append(fx, fixture{name: fmt.Sprintf("short-sentinel-%d", cut), run: func(e *fxEnv, dir string) (*fxWant, string, error) {
			_, _, data, err := e.threeBase(dir)
			if err != nil {
				return nil, "", err
			}
			db, err := writeVariant(dir, "db", data[:int64(len(data))-cut])
			return &fxWant{keys: keysOf("a", "1", "b", "2", "c", "3"), tail: "SHORT_SENTINEL", check: "OK", infoHas: []string{"tail=SHORT_SENTINEL"}}, db, err
		}})
	}

	// One payload byte flipped in a complete last frame: the frame is absent, never half-applied.
	fx = append(fx, fixture{name: "bad-crc", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		_, im, data, err := e.threeBase(dir)
		if err != nil {
			return nil, "", err
		}
		bad := append([]byte(nil), data...)
		bad[lastFrame(im).Ops[0].ValueOff] ^= 0x01
		db, err := writeVariant(dir, "db", bad)
		// SPEC 13.6: a complete but invalid frame is CORRUPT for check; open and dump treat it as the end of the log.
		return &fxWant{keys: keysOf("a", "1", "b", "2"), tail: "TORN", check: "CORRUPT", infoHas: []string{"frame_crc"}}, db, err
	}})

	// A write after a damaged tail must repair it first (P4): no stale byte may ever sit behind a new frame.
	fx = append(fx, fixture{name: "short-sentinel-then-put", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		_, _, data, err := e.threeBase(dir)
		if err != nil {
			return nil, "", err
		}
		db, err := writeVariant(dir, "db", data[:len(data)-3])
		if err != nil {
			return nil, "", err
		}
		if err := e.kv("put", db, "d", "4"); err != nil {
			return nil, "", err
		}
		return &fxWant{keys: keysOf("a", "1", "b", "2", "c", "3", "d", "4"), tail: "COMPLETE", check: "OK", infoHas: []string{"frames=4"}}, db, nil
	}})
	fx = append(fx, fixture{name: "torn-then-put", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		_, im, data, err := e.threeBase(dir)
		if err != nil {
			return nil, "", err
		}
		l := lastFrame(im)
		// The torn region is made much longer than the frame the next put will write, so a write that
		// does not truncate first would leave stale bytes behind the new frame (P4).
		torn := append(append([]byte(nil), data[:l.Off+frameHdrLen+int64(l.PayloadLen)/2]...), bytes.Repeat([]byte{0xAB}, 400)...)
		db, err := writeVariant(dir, "db", torn)
		if err != nil {
			return nil, "", err
		}
		if err := e.kv("put", db, "d", "4"); err != nil {
			return nil, "", err
		}
		return &fxWant{keys: keysOf("a", "1", "b", "2", "d", "4"), tail: "COMPLETE", check: "OK", infoHas: []string{"frames=3"}}, db, nil
	}})

	// Stale bytes behind the tail: a frame with another epoch, or with a sequence gap, is never accepted,
	// and the next write removes it before appending.
	staleFrame := func(name string, mk func(epoch uint64, nextSeq uint64) []byte) fixture {
		return fixture{name: name, run: func(e *fxEnv, dir string) (*fxWant, string, error) {
			_, im, data, err := e.threeBase(dir)
			if err != nil {
				return nil, "", err
			}
			stale := mk(im.Super.Epoch, uint64(len(im.Frames))+1)
			db, err := writeVariant(dir, "db", append(append([]byte(nil), data...), stale...))
			// open and dump treat the stale frame as the end of the log; check calls it CORRUPT (SPEC 13.6).
			return &fxWant{keys: keysOf("a", "1", "b", "2", "c", "3"), tail: "TORN", check: "CORRUPT"}, db, err
		}}
	}
	zput := []FileOp{{Key: []byte("z"), Value: bytes.Repeat([]byte("9"), 300)}} // longer than the frame of the next put
	fx = append(fx,
		staleFrame("stale-epoch-frame", func(epoch, seq uint64) []byte { return EncodeBatchFrame(epoch+1, seq, zput) }),
		staleFrame("stale-seq-gap-frame", func(epoch, seq uint64) []byte { return EncodeBatchFrame(epoch, seq+1, zput) }),
		fixture{name: "stale-frame-then-put", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
			_, im, data, err := e.threeBase(dir)
			if err != nil {
				return nil, "", err
			}
			stale := EncodeBatchFrame(im.Super.Epoch+1, uint64(len(im.Frames))+1, zput)
			db, err := writeVariant(dir, "db", append(append([]byte(nil), data...), stale...))
			if err != nil {
				return nil, "", err
			}
			if err := e.kv("put", db, "d", "4"); err != nil {
				return nil, "", err
			}
			return &fxWant{keys: keysOf("a", "1", "b", "2", "c", "3", "d", "4"), tail: "COMPLETE", check: "OK", infoHas: []string{"frames=4"}}, db, nil
		}},
	)

	// Superblocks: a torn newest slot falls back to the older one; the cap persists; no slot is an error.
	fx = append(fx, fixture{name: "torn-newest-sb", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		db, _, _, err := e.threeBase(dir)
		if err != nil {
			return nil, "", err
		}
		if err := e.kv("open", db, "--max-bytes", "5000000"); err != nil {
			return nil, "", err
		}
		im, data, err := readImage(db)
		if err != nil {
			return nil, "", err
		}
		if !im.Slots[1].Valid || im.Slots[1].Generation != 2 || im.Slots[1].MaxBytes != 5000000 || im.Chosen != 1 {
			return nil, "", fmt.Errorf("precondition: expected slot B generation 2 with the cap after `open --max-bytes`, got %+v / %+v", im.Slots[0], im.Slots[1])
		}
		bad := append([]byte(nil), data...)
		bad[sbSlotSize+20] ^= 0xff // fixture torn-newest-sb flips byte 20 of the newest slot
		out, err := writeVariant(dir, "db", bad)
		return &fxWant{keys: keysOf("a", "1", "b", "2", "c", "3"), tail: "COMPLETE", check: "OK", maxBytes: "0", infoHas: []string{"slot B invalid"}}, out, err
	}})
	fx = append(fx, fixture{name: "persisted-cap", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		db, _, _, err := e.threeBase(dir)
		if err != nil {
			return nil, "", err
		}
		if err := e.kv("open", db, "--max-bytes", "5000000"); err != nil {
			return nil, "", err
		}
		return &fxWant{keys: keysOf("a", "1", "b", "2", "c", "3"), tail: "COMPLETE", check: "OK", maxBytes: "5000000", infoHas: []string{"max_bytes=5000000"}}, db, nil
	}})
	fx = append(fx, fixture{name: "no-valid-superblock", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		_, _, data, err := e.threeBase(dir)
		if err != nil {
			return nil, "", err
		}
		bad := append([]byte(nil), data...)
		for i := 0; i < firstFrameOff; i++ {
			bad[i] = 0
		}
		db, err := writeVariant(dir, "db", bad)
		return &fxWant{openFails: true}, db, err
	}})

	// Checkpoints. Compaction leaves slot A pointing at a KEYDIR frame (SPEC 10).
	compacted := func(e *fxEnv, dir string) (string, error) {
		db, _, _, err := e.threeBase(dir)
		if err != nil {
			return "", err
		}
		if _, err := e.serveText(db, "compact"); err != nil {
			return "", err
		}
		im, _, err := readImage(db)
		if err != nil {
			return "", err
		}
		if im.Super.CkptOff == 0 || lastFrame(im).Type != typeKeydir {
			return "", fmt.Errorf("precondition: compaction should leave a checkpoint, slot A=%+v last frame type %d", im.Super, lastFrame(im).Type)
		}
		return db, nil
	}
	fx = append(fx, fixture{name: "ckpt-plus-torn-tail", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		db, err := compacted(e, dir)
		if err != nil {
			return nil, "", err
		}
		for _, kv := range [][2]string{{"p", "4"}, {"q", "5"}} {
			if err := e.kv("put", db, kv[0], kv[1]); err != nil {
				return nil, "", err
			}
		}
		im, data, err := readImage(db)
		if err != nil {
			return nil, "", err
		}
		l := lastFrame(im)
		out, err := writeVariant(dir, "db", data[:l.Off+frameHdrLen+int64(l.PayloadLen)/2])
		return &fxWant{keys: keysOf("a", "1", "b", "2", "c", "3", "p", "4"), tail: "TORN", check: "OK", infoHas: []string{"checkpoint ok"}}, out, err
	}})
	fx = append(fx, fixture{name: "bad-ckpt-fallback", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		db, err := compacted(e, dir)
		if err != nil {
			return nil, "", err
		}
		im, data, err := readImage(db)
		if err != nil {
			return nil, "", err
		}
		bad := append([]byte(nil), data...)
		bad[lastFrame(im).Off+frameHdrLen+20] ^= 0x01 // a byte inside the KEYDIR payload
		out, err := writeVariant(dir, "db", bad)
		// Slot A still points at the damaged KEYDIR: recovery must fall back to a full scan and find
		// the same data in the packed BATCH frames before it.
		return &fxWant{keys: keysOf("a", "1", "b", "2", "c", "3"), tail: "TORN", check: "CORRUPT"}, out, err
	}})

	// C6: a clean close (serve reads EOF) stops the committer and fsyncs; everything it ACKed is there,
	// including a text-mode batch (begin ... commit = one frame).
	fx = append(fx, fixture{name: "clean-close", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		db := filepath.Join(dir, "db")
		out, err := e.serveText(db, "put a 1", "put b 2", "begin", "put c 3", "put d 4", "commit")
		if err != nil {
			return nil, "", err
		}
		w := &fxWant{keys: keysOf("a", "1", "b", "2", "c", "3", "d", "4"), tail: "COMPLETE", check: "OK", infoHas: []string{"frames=3"}}
		if n := strings.Count(out, "ACK"); n != 3 {
			w.pre = append(w.pre, fmt.Sprintf("serve ACKed %d of 3 requests before closing: %q", n, truncate(out, 120)))
		}
		return w, db, nil
	}})

	// E4: COMPACT with a cap smaller than the compacted image is refused with FULL BEFORE the temp file is
	// created (SPEC 10 step 1), and the data file is left byte-identical.
	fx = append(fx, fixture{name: "compact-refused-at-small-cap", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
		db := filepath.Join(dir, "db")
		big := strings.Repeat("x", 30000)
		for _, k := range []string{"a", "b", "c", "a"} { // "a" twice: garbage, so a compaction would have something to do
			if err := e.kv("put", db, k, big); err != nil {
				return nil, "", err
			}
		}
		if err := e.kv("open", db, "--max-bytes", "20000"); err != nil { // far below the 90,000 bytes of live data
			return nil, "", err
		}
		before, beforeSize, _ := fileDigest(db)
		out, err := e.serveText(db, "compact")
		if err != nil {
			return nil, "", err
		}
		w := &fxWant{keys: keysOf("a", big, "b", big, "c", big), tail: "COMPLETE", check: "OK", maxBytes: "20000"}
		if !strings.Contains(out, "FULL") {
			w.pre = append(w.pre, fmt.Sprintf("COMPACT under a 20000-byte cap should answer ERR ... FULL, got %q", truncate(strings.TrimSpace(out), 120)))
		}
		if fileExists(db + ".compact.tmp") {
			w.pre = append(w.pre, "COMPACT was refused but path.compact.tmp was created: the precheck must come first (E4)")
		}
		if after, afterSize, _ := fileDigest(db); after != before || afterSize != beforeSize {
			w.pre = append(w.pre, fmt.Sprintf("a refused COMPACT changed the data file (size %d -> %d)", beforeSize, afterSize))
		}
		return w, db, nil
	}})

	// Stale temp files are never authoritative and are removed by open (SPEC 6.2).
	for _, suffix := range []string{".compact.tmp", ".create.tmp"} {
		suffix := suffix
		fx = append(fx, fixture{name: "stale" + suffix[:len(suffix)-4] + "-tmp", run: func(e *fxEnv, dir string) (*fxWant, string, error) {
			db, _, _, err := e.threeBase(dir)
			if err != nil {
				return nil, "", err
			}
			tmp := db + suffix
			garbage := make([]byte, 20000)
			for i := range garbage {
				garbage[i] = byte(i*7 + 3)
			}
			if err := os.WriteFile(tmp, garbage, 0o644); err != nil {
				return nil, "", err
			}
			return &fxWant{keys: keysOf("a", "1", "b", "2", "c", "3"), tail: "COMPLETE", check: "OK", gone: []string{tmp}}, db, nil
		}})
	}
	return fx
}
