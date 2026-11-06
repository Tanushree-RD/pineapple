// Command fakekvdb is a TEST DOUBLE for HYDRA's own tests. It speaks just enough of the kvdb CLI
// (serve --b64, open, check, dump) to let the harness run end to end before the real engine exists,
// and it can be told to misbehave so we can prove HYDRA catches each bug.
//
// FAKE_MODE selects the behavior:
//
//	durable    (default) one CRC-protected line per transaction, written before the ACK. HYDRA must PASS.
//	volatile   keeps everything in RAM, persists nothing.                      HYDRA must FAIL (ACKed data lost).
//	droplast   recovery ignores the newest record.                              HYDRA must FAIL (ACKed write lost).
//	resurrect  recovery ignores deletes.                                        HYDRA must FAIL (deleted key returns).
//	nonatomic  writes a batch one op per line, with a tiny pause between ops.   HYDRA must FAIL (partial batch).
//	noshrink   COMPACT is ACKed but the file is not rewritten.                  HYDRA must FAIL in compaction mode (no shrink).
//	garble     dump flips one byte of the first non-empty value.                HYDRA must FAIL (corrupt value), at iteration 1.
//
// This is not the real database and is never built by `make`.
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

var mode = func() string {
	if m := os.Getenv("FAKE_MODE"); m != "" {
		return m
	}
	return "durable"
}()

var crcTable = crc32.MakeTable(crc32.Castagnoli)

type op struct {
	del bool
	key []byte
	val []byte
}

type store struct {
	m       map[string][]byte
	goodLen int64 // length of the valid prefix of the file
}

func encodeTx(ops []op) string {
	var parts []string
	for _, o := range ops {
		if o.del {
			parts = append(parts, "D:"+hex.EncodeToString(o.key))
		} else {
			parts = append(parts, "P:"+hex.EncodeToString(o.key)+":"+hex.EncodeToString(o.val))
		}
	}
	body := strings.Join(parts, ",")
	return fmt.Sprintf("T %08x %s\n", crc32.Checksum([]byte(body), crcTable), body)
}

func decodeTx(line string) ([]op, bool) {
	f := strings.SplitN(strings.TrimSuffix(line, "\n"), " ", 3)
	if len(f) != 3 || f[0] != "T" {
		return nil, false
	}
	want, err := strconv.ParseUint(f[1], 16, 32)
	if err != nil || crc32.Checksum([]byte(f[2]), crcTable) != uint32(want) {
		return nil, false
	}
	var ops []op
	for _, p := range strings.Split(f[2], ",") {
		switch {
		case strings.HasPrefix(p, "D:"):
			k, err := hex.DecodeString(p[2:])
			if err != nil {
				return nil, false
			}
			ops = append(ops, op{del: true, key: k})
		case strings.HasPrefix(p, "P:"):
			kv := strings.SplitN(p[2:], ":", 2)
			if len(kv) != 2 {
				return nil, false
			}
			k, e1 := hex.DecodeString(kv[0])
			v, e2 := hex.DecodeString(kv[1])
			if e1 != nil || e2 != nil {
				return nil, false
			}
			ops = append(ops, op{key: k, val: v})
		default:
			return nil, false
		}
	}
	return ops, true
}

// recoverFile reads the file without writing it and returns the store plus the valid prefix length.
func recoverFile(path string) (*store, error) {
	s := &store{m: map[string][]byte{}}
	if mode == "volatile" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var txs [][]op
	off := 0
	for off < len(data) {
		nl := bytes.IndexByte(data[off:], '\n')
		if nl < 0 {
			break // torn tail
		}
		ops, ok := decodeTx(string(data[off : off+nl+1]))
		if !ok {
			break
		}
		txs = append(txs, ops)
		off += nl + 1
	}
	s.goodLen = int64(off)
	if mode == "droplast" && len(txs) > 0 {
		txs = txs[:len(txs)-1]
	}
	for _, ops := range txs {
		apply(s.m, ops)
	}
	return s, nil
}

func apply(m map[string][]byte, ops []op) {
	for _, o := range ops {
		if o.del {
			if mode != "resurrect" {
				delete(m, string(o.key))
			}
		} else {
			m[string(o.key)] = o.val
		}
	}
}

func esc(b []byte) string {
	ok := len(b) > 0 && string(b) != "NOTFOUND" && !strings.HasPrefix(string(b), "hex:")
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

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: fakekvdb serve|open|check|dump <path> ...")
		os.Exit(1)
	}
	cmd, path := os.Args[1], os.Args[2]
	switch cmd {
	case "open":
		s, err := recoverFile(path)
		must(err)
		fmt.Printf("keys=%d recovery_ms=0.1 tail=COMPLETE max_bytes=0\n", len(s.m))
	case "check":
		_, err := recoverFile(path)
		must(err)
		fmt.Println("OK")
	case "dump":
		s, err := recoverFile(path)
		must(err)
		keys := make([]string, 0, len(s.m))
		for k := range s.m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		w := bufio.NewWriter(os.Stdout)
		garbled := false
		for _, k := range keys {
			v := s.m[k]
			if mode == "garble" && !garbled && len(v) > 0 {
				v = append([]byte(nil), v...)
				v[len(v)-1] ^= 0x01
				garbled = true
			}
			fmt.Fprintf(w, "%s\t%s\n", esc([]byte(k)), esc(v))
		}
		must(w.Flush())
	case "serve":
		serve(path)
	default:
		fmt.Fprintln(os.Stderr, "unknown command", cmd)
		os.Exit(1)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR", err)
		os.Exit(1)
	}
}

func serve(servePath string) {
	path := servePath
	s, err := recoverFile(path)
	must(err)
	var f *os.File
	if mode != "volatile" {
		f, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
		must(err)
		must(f.Truncate(s.goodLen)) // the first write repairs the torn tail
		_, err = f.Seek(s.goodLen, io.SeekStart)
		must(err)
	}
	out := bufio.NewWriter(os.Stdout)
	fmt.Fprintf(out, "READY keys=%d recovery_ms=0.1 max_bytes=0\n", len(s.m))
	out.Flush()

	commit := func(ops []op) {
		if f != nil {
			if mode == "nonatomic" {
				for _, o := range ops {
					_, err := f.WriteString(encodeTx([]op{o}))
					must(err)
					time.Sleep(150 * time.Microsecond)
				}
			} else {
				_, err := f.WriteString(encodeTx(ops))
				must(err)
			}
		}
		apply(s.m, ops)
	}
	ack := func(id string) { fmt.Fprintf(out, "ACK %s\n", id); out.Flush() }

	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	readLine := func() (string, bool) {
		l, err := in.ReadString('\n')
		if err != nil {
			return "", false
		}
		return strings.TrimSuffix(l, "\n"), true
	}
	dec := base64.StdEncoding
	val := func(tok string) []byte {
		if tok == "-" {
			return []byte{}
		}
		b, err := dec.DecodeString(tok)
		must(err)
		return b
	}
	for {
		line, ok := readLine()
		if !ok {
			return
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			fmt.Fprintf(out, "ERR 0 INVALID\n")
			out.Flush()
			continue
		}
		id, cmd := fields[0], fields[2]
		switch cmd {
		case "PUT":
			k, _ := dec.DecodeString(fields[3])
			commit([]op{{key: k, val: val(fields[4])}})
			ack(id)
		case "DEL":
			k, _ := dec.DecodeString(fields[3])
			commit([]op{{del: true, key: k}})
			ack(id)
		case "BATCH":
			n, _ := strconv.Atoi(fields[3])
			var ops []op
			for i := 0; i < n; i++ {
				l, ok := readLine()
				if !ok {
					return
				}
				p := strings.Fields(l)
				k, _ := dec.DecodeString(p[1])
				if p[0] == "DEL" {
					ops = append(ops, op{del: true, key: k})
				} else {
					ops = append(ops, op{key: k, val: val(p[2])})
				}
			}
			commit(ops)
			ack(id)
		case "COMPACT":
			if f != nil && mode != "noshrink" {
				// Rewrite the log as one record holding the live state: temp file, fsync-free, rename.
				var live []op
				for k, v := range s.m {
					live = append(live, op{key: []byte(k), val: v})
				}
				tmp := path + ".compact.tmp"
				if len(live) > 0 {
					must(os.WriteFile(tmp, []byte(encodeTx(live)), 0o644))
				} else {
					must(os.WriteFile(tmp, nil, 0o644))
				}
				must(os.Rename(tmp, path))
				f.Close()
				nf, err := os.OpenFile(path, os.O_RDWR, 0o644)
				must(err)
				_, err = nf.Seek(0, io.SeekEnd)
				must(err)
				f = nf
			}
			ack(id)
		default:
			fmt.Fprintf(out, "ERR %s INVALID\n", id)
			out.Flush()
		}
	}
}
