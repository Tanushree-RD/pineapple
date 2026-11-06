package main

import (
	"strings"
	"testing"
)

func TestParseDumpBasics(t *testing.T) {
	in := "hex:0001\tplain text with spaces\n" +
		"alpha\thex:00ff10\n" +
		"beta\thex:\n"
	m, err := ParseDump(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 {
		t.Fatalf("want 3 keys, got %d", len(m))
	}
	if string(m["alpha"]) != "\x00\xff\x10" {
		t.Fatalf("alpha = %x", m["alpha"])
	}
	if v, ok := m["beta"]; !ok || v == nil || len(v) != 0 {
		t.Fatalf("empty value must parse as present+empty, got %#v ok=%v", v, ok)
	}
	if string(m["\x00\x01"]) != "plain text with spaces" {
		t.Fatalf("binary key lost: %#v", m)
	}
}

func TestParseDumpEmpty(t *testing.T) {
	m, err := ParseDump(strings.NewReader(""))
	if err != nil || len(m) != 0 {
		t.Fatalf("empty dump: %v %v", m, err)
	}
}

func TestParseDumpRejectsMalformed(t *testing.T) {
	bad := map[string]string{
		"no tab":            "keyonly\n",
		"no newline":        "a\tb",
		"duplicate":         "a\tx\na\ty\n",
		"descending":        "b\tx\na\ty\n",
		"uppercase hex":     "a\thex:FF\n",
		"odd hex":           "a\thex:abc\n",
		"raw tab in value":  "a\tx\ty\n",
		"raw backslash":     "a\tx\\y\n",
		"raw NOTFOUND":      "a\tNOTFOUND\n",
		"empty value token": "a\t\n",
		"empty key":         "hex:\tx\n",
		"control byte":      "a\tx\x01y\n",
	}
	for name, in := range bad {
		if _, err := ParseDump(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted %q", name, in)
		}
	}
}

func TestDecodeTokenLiteralHexPrefixValue(t *testing.T) {
	// A value that literally starts with "hex:" must have been printed as hex, so a raw one is
	// decoded as hex and fails when it is not valid hex.
	if _, err := decodeToken("hex:zz"); err == nil {
		t.Fatal("hex:zz accepted")
	}
}
