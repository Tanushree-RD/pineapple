package main

import (
	"bytes"
	"testing"
)

func TestEscapeOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "empty value",
			input:    []byte(""),
			expected: "hex:",
		},
		{
			name:     "plain ascii",
			input:    []byte("hello world 123!"),
			expected: "hello world 123!",
		},
		{
			name:     "exact NOTFOUND",
			input:    []byte("NOTFOUND"),
			expected: "hex:4e4f54464f554e44",
		},
		{
			name:     "starts with hex:",
			input:    []byte("hex:1234"),
			expected: "hex:6865783a31323334",
		},
		{
			name:     "contains tab",
			input:    []byte("hello\tworld"),
			expected: "hex:68656c6c6f09776f726c64",
		},
		{
			name:     "contains newline",
			input:    []byte("hello\nworld"),
			expected: "hex:68656c6c6f0a776f726c64",
		},
		{
			name:     "contains carriage return",
			input:    []byte("hello\rworld"),
			expected: "hex:68656c6c6f0d776f726c64",
		},
		{
			name:     "contains backslash",
			input:    []byte("hello\\world"),
			expected: "hex:68656c6c6f5c776f726c64",
		},
		{
			name:     "binary null byte",
			input:    []byte{0x00, 0x01, 0x02},
			expected: "hex:000102",
		},
		{
			name:     "binary high bytes",
			input:    []byte{0x80, 0xfe, 0xff},
			expected: "hex:80feff",
		},
		{
			name:     "printable range edges space and tilde",
			input:    []byte(" ~"),
			expected: " ~",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EncodeOutput(tc.input)
			if got != tc.expected {
				t.Fatalf("EncodeOutput(%q) = %q, expected %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestDecodeInput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []byte
	}{
		{
			name:     "plain string",
			input:    "plain-key",
			expected: []byte("plain-key"),
		},
		{
			name:     "hex decoded",
			input:    "hex:00ff10",
			expected: []byte{0x00, 0xff, 0x10},
		},
		{
			name:     "hex empty",
			input:    "hex:",
			expected: []byte{},
		},
		{
			name:     "hex case insensitive input",
			input:    "hex:00FF10",
			expected: []byte{0x00, 0xff, 0x10},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeInput(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(got, tc.expected) {
				t.Fatalf("DecodeInput(%q) = %v, expected %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	samples := [][]byte{
		[]byte(""),
		[]byte("simple"),
		[]byte("NOTFOUND"),
		[]byte("hex:abc"),
		[]byte("with\nnewline"),
		[]byte("with\ttab"),
		[]byte("with\\backslash"),
		{0x00, 0xff, 0x42},
	}

	for _, original := range samples {
		encoded := EncodeOutput(original)
		decoded, err := DecodeInput(encoded)
		if err != nil {
			t.Fatalf("DecodeInput failed on %q: %v", encoded, err)
		}
		if !bytes.Equal(decoded, original) {
			t.Fatalf("Roundtrip failed for %v: encoded %q, decoded %v", original, encoded, decoded)
		}
	}
}
