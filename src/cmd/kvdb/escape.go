package main

import (
	"encoding/hex"
	"strings"
)

// DecodeInput parses an input argument (key or value).
// If the argument starts with "hex:", it is hex-decoded.
// Otherwise, it is taken as raw text bytes.
func DecodeInput(s string) ([]byte, error) {
	if strings.HasPrefix(s, "hex:") {
		return hex.DecodeString(s[4:])
	}
	return []byte(s), nil
}

// EncodeOutput formats a key or value for printing according to SPEC 8.3:
// A key or value is printed raw only if all of these hold:
//   - it is non-empty
//   - it is printable ASCII (0x21 to 0x7E plus space 0x20)
//   - it contains no tab (\t), newline (\n) or backslash (\)
//   - it does not start with "hex:"
//   - it is not exactly "NOTFOUND"
//
// Otherwise it is printed as "hex:<lowercase hex>". The empty value prints as "hex:".
func EncodeOutput(b []byte) string {
	if len(b) == 0 {
		return "hex:"
	}

	s := string(b)
	if s == "NOTFOUND" || strings.HasPrefix(s, "hex:") {
		return "hex:" + hex.EncodeToString(b)
	}

	for i := 0; i < len(b); i++ {
		c := b[i]
		if c < 0x20 || c > 0x7E || c == '\\' {
			return "hex:" + hex.EncodeToString(b)
		}
	}

	return s
}
