package main

import (
	"strings"
	"testing"

	"github.com/chethan-9817/pineapple/src/internal/tui"
)

func TestDashCountsAndRendersAtTheRequestedWidth(t *testing.T) {
	tui.SetColor(false)
	d := newDash("default", "seed 1", 10)
	for i := 1; i <= 4; i++ {
		d.onEvent(parseLine(strings.Replace("iter N/10 seed=1 mode=default kill_ms=100 acked=50 inflight=3 rejected=0 keys=900 open_ms=2 check=OK PASS", "N", string(rune('0'+i)), 1)))
	}
	d.onEvent(event{kind: evFail, line: "FAIL iter 5/10"})
	if d.done != 4 || d.pass != 4 || d.fail != 1 {
		t.Fatalf("done=%d pass=%d fail=%d", d.done, d.pass, d.fail)
	}
	for _, w := range []int{60, 80, 110} {
		for i, l := range d.lines(w) {
			if tui.VisLen(l) != w {
				t.Fatalf("width %d line %d has %d columns: %q", w, i, tui.VisLen(l), l)
			}
		}
	}
	joined := strings.Join(d.lines(80), "\n")
	for _, want := range []string{"HYDRA", "default", "4/10", "1 FAILED", "kill ms", "ACKed"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("dashboard lacks %q:\n%s", want, joined)
		}
	}
}

func TestDashCaseEventsAdvanceTheBar(t *testing.T) {
	tui.SetColor(false)
	d := newDash("fixtures", "", 31)
	d.onEvent(parseLine("fixture clean-empty                        PASS"))
	d.onEvent(parseLine("fixture bad-crc                            FAIL"))
	if d.done != 2 || d.pass != 1 || d.fail != 1 || d.last != "fixture bad-crc" {
		t.Fatalf("%+v", d)
	}
}

func TestScaleArgsQuick(t *testing.T) {
	in := []string{"--iterations", "200", "--seed", "1"}
	if got := scaleArgs(in, true); got[1] != "20" || in[1] != "200" {
		t.Fatalf("quick: %v (input must not change: %v)", got, in)
	}
	if got := scaleArgs([]string{"--iterations", "10"}, true); got[1] != "3" {
		t.Fatalf("minimum is 3, got %v", got)
	}
	if got := scaleArgs(in, false); got[1] != "200" {
		t.Fatalf("full: %v", got)
	}
	if iterationsOf(in) != 200 || iterationsOf([]string{"--mode", "sweep"}) != 0 {
		t.Fatal("iterationsOf")
	}
}

func TestSlug(t *testing.T) {
	if slug("default 200 / seed 7") != "default-200-seed-7" {
		t.Fatal(slug("default 200 / seed 7"))
	}
}

func TestMergeArgsUserFlagsOverrideDefaults(t *testing.T) {
	got := strings.Join(mergeArgs([]string{"--iterations", "100"}, []string{"--iterations", "30", "--writers", "4"}), " ")
	if got != "--iterations 30 --writers 4" {
		t.Fatalf("got %q", got)
	}
	got = strings.Join(mergeArgs([]string{"--mode", "sweep"}, []string{"--cp", "CP-W05"}), " ")
	if got != "--mode sweep --cp CP-W05" {
		t.Fatalf("got %q", got)
	}
	got = strings.Join(mergeArgs([]string{"--iterations", "100", "--mode", "large-values"}, nil), " ")
	if got != "--iterations 100 --mode large-values" {
		t.Fatalf("got %q", got)
	}
}
