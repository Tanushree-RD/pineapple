package main

// Parsing of `kvdb dump` output (MASTER_PROMPT 8.3). HYDRA parses it with its own code: the
// database's output is an observation, never trusted as truth.
//
// Each line is "<key>\t<value>". A token is printed raw only when it is non-empty, printable ASCII
// (0x21..0x7E plus space), has no tab/newline/backslash, does not start with "hex:" and is not
// exactly "NOTFOUND"; otherwise it is "hex:<lowercase hex>". The empty value prints as "hex:".
// Keys are printed in ascending byte order.

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// decodeToken reverses the output escaping.
func decodeToken(tok string) ([]byte, error) {
	if strings.HasPrefix(tok, "hex:") {
		h := tok[4:]
		if h != strings.ToLower(h) {
			return nil, fmt.Errorf("hex token %q is not lowercase", truncate(tok, 60))
		}
		b, err := hex.DecodeString(h)
		if err != nil {
			return nil, fmt.Errorf("bad hex token %q: %v", truncate(tok, 60), err)
		}
		return b, nil
	}
	if tok == "" {
		return nil, fmt.Errorf("empty token (an empty value must print as \"hex:\")")
	}
	if tok == "NOTFOUND" {
		return nil, fmt.Errorf("raw NOTFOUND token (must be printed as hex)")
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if c < 0x20 || c > 0x7e || c == '\\' {
			return nil, fmt.Errorf("raw token %q contains byte 0x%02x that must be escaped", truncate(tok, 60), c)
		}
	}
	return []byte(tok), nil
}

// ParseDump parses the whole dump. It also verifies what the CLI promises about the dump itself:
// well-formed lines, no duplicate keys, strictly ascending key order.
func ParseDump(r io.Reader) (map[string][]byte, error) {
	out := make(map[string][]byte)
	br := bufio.NewReaderSize(r, 1<<20)
	var prev []byte
	havePrev := false
	for lineNo := 1; ; lineNo++ {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		if len(line) == 0 && err == io.EOF {
			break
		}
		if !strings.HasSuffix(line, "\n") {
			return nil, fmt.Errorf("dump line %d: no trailing newline (truncated output)", lineNo)
		}
		line = line[:len(line)-1]
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("dump line %d: no tab separator: %q", lineNo, truncate(line, 80))
		}
		k, e1 := decodeToken(line[:tab])
		if e1 != nil {
			return nil, fmt.Errorf("dump line %d key: %v", lineNo, e1)
		}
		v, e2 := decodeToken(line[tab+1:])
		if e2 != nil && line[tab+1:] != "hex:" {
			return nil, fmt.Errorf("dump line %d value: %v", lineNo, e2)
		}
		if len(k) == 0 {
			return nil, fmt.Errorf("dump line %d: empty key", lineNo)
		}
		if v == nil {
			v = []byte{}
		}
		if havePrev {
			switch c := bytes.Compare(prev, k); {
			case c == 0:
				return nil, fmt.Errorf("dump line %d: duplicate key %s", lineNo, hexKey(k))
			case c > 0:
				return nil, fmt.Errorf("dump line %d: keys not in ascending byte order (%s after %s)", lineNo, hexKey(k), hexKey(prev))
			}
		}
		prev, havePrev = k, true
		out[string(k)] = v
		if err == io.EOF {
			break
		}
	}
	return out, nil
}
