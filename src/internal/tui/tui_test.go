package tui

import (
	"strings"
	"testing"
	"time"
)

func TestVisLenIgnoresEscapes(t *testing.T) {
	SetColor(true)
	defer SetColor(false)
	s := Green("abc") + Bold("de")
	if VisLen(s) != 5 {
		t.Fatalf("VisLen(%q) = %d, want 5", s, VisLen(s))
	}
	if Strip(s) != "abcde" {
		t.Fatalf("Strip = %q", Strip(s))
	}
}

func TestNoColorIsPlain(t *testing.T) {
	SetColor(false)
	if Red("x") != "x" || Bold("x") != "x" || Gradient("abc", 1, 9) != "abc" {
		t.Fatal("with colors off every style must return the text unchanged")
	}
}

func TestBarHasExactWidthAndClamps(t *testing.T) {
	for _, c := range []bool{false, true} {
		SetColor(c)
		for _, f := range []float64{-1, 0, 0.001, 0.25, 0.5, 0.999, 1, 7} {
			if n := VisLen(Bar(f, 30)); n != 30 {
				t.Fatalf("color=%v frac=%v: width %d, want 30", c, f, n)
			}
		}
	}
	SetColor(false)
	if !strings.HasPrefix(Bar(1, 10), strings.Repeat("█", 10)) || strings.Contains(Bar(0, 10), "█") {
		t.Fatalf("full and empty bars wrong: %q %q", Bar(1, 10), Bar(0, 10))
	}
}

func TestSparkScalesToItsOwnRange(t *testing.T) {
	SetColor(false)
	s := Spark([]float64{1, 2, 3, 4, 5, 6, 7, 8}, 8)
	if s != "▁▂▃▄▅▆▇█" {
		t.Fatalf("spark = %q", s)
	}
	if got := Spark(nil, 5); got != "     " {
		t.Fatalf("empty spark = %q", got)
	}
	if n := VisLen(Spark([]float64{3, 3, 3}, 6)); n != 6 {
		t.Fatalf("flat spark width = %d", n)
	}
	if n := VisLen(Spark(make([]float64, 100), 10)); n != 10 {
		t.Fatalf("long input must be cut to the width, got %d", n)
	}
}

func TestBoxLinesAllHaveTheSameWidth(t *testing.T) {
	SetColor(true)
	defer SetColor(false)
	lines := Box("title", []string{"short", Green("colored line"), strings.Repeat("x", 200)}, 40, nil)
	for i, l := range lines {
		if VisLen(l) != 40 {
			t.Fatalf("line %d has width %d, want 40: %q", i, VisLen(l), Strip(l))
		}
	}
	plain := Box("", []string{"a"}, 20, nil)
	if VisLen(plain[0]) != 20 {
		t.Fatalf("untitled top edge width %d", VisLen(plain[0]))
	}
}

func TestTruncAndPad(t *testing.T) {
	if Trunc("abcdef", 4) != "abc…" || Trunc("abc", 4) != "abc" {
		t.Fatal("Trunc")
	}
	if PadRight("ab", 5) != "ab   " || PadRight("abcdef", 3) != "abcdef" {
		t.Fatal("PadRight")
	}
}

func TestFormatting(t *testing.T) {
	if Dur(75*time.Second) != "1:15" || Dur(3725*time.Second) != "1:02:05" || Dur(-time.Second) != "0:00" {
		t.Fatalf("Dur: %s %s", Dur(75*time.Second), Dur(3725*time.Second))
	}
	if Bytes(512) != "512 B" || Bytes(1536) != "1.5 KiB" || Bytes(3<<20) != "3.0 MiB" {
		t.Fatalf("Bytes: %s %s %s", Bytes(512), Bytes(1536), Bytes(3<<20))
	}
}

func TestBannerIsFiveRowsOfEqualWidth(t *testing.T) {
	SetColor(false)
	for _, word := range []string{"HYDRA", "LEAPFROG-R"} {
		b := Banner(word, 33, 39)
		if len(b) != 5 {
			t.Fatalf("%s: %d rows", word, len(b))
		}
		w := VisLen(b[0])
		for i, r := range b {
			if VisLen(r) != w {
				t.Fatalf("%s row %d width %d, want %d", word, i, VisLen(r), w)
			}
		}
		if !strings.Contains(strings.Join(b, ""), "█") {
			t.Fatalf("%s banner has no glyph pixels", word)
		}
	}
}

func TestMenuNumberedFallback(t *testing.T) {
	// Under `go test` stdin is not a terminal, so Menu must use the numbered list.
	SetColor(false)
	var out strings.Builder
	items := []MenuItem{{"one", "first"}, {"two", "second"}}
	idx, ok := Menu(&out, bufioReader("2\n"), "pick", items)
	if !ok || idx != 1 {
		t.Fatalf("got %d %v, want 1 true", idx, ok)
	}
	if _, ok := Menu(&out, bufioReader("\n"), "pick", items); ok {
		t.Fatal("empty input must quit")
	}
	if _, ok := Menu(&out, bufioReader("9\n"), "pick", items); ok {
		t.Fatal("out-of-range input must quit")
	}
}
