package main

import "testing"

func TestParseIterLine(t *testing.T) {
	ev := parseLine("iter 17/500 seed=12345 mode=default kill_ms=134 acked=212 inflight=6 rejected=0 keys=8123 open_ms=41 check=OK PASS")
	if ev.kind != evIter || !ev.pass || ev.i != 17 || ev.n != 500 {
		t.Fatalf("%+v", ev)
	}
	if ev.kv["kill_ms"] != "134" || ev.kv["acked"] != "212" || ev.kv["keys"] != "8123" || ev.kv["mode"] != "default" {
		t.Fatalf("kv %v", ev.kv)
	}
}

func TestParseIterLineWithModeExtras(t *testing.T) {
	ev := parseLine("iter 10/30 seed=7 mode=compaction kill_ms=56 acked=54 inflight=0 rejected=0 keys=1530 open_ms=0 shrink=387309->358946 check=OK PASS")
	if ev.kind != evIter || ev.kv["shrink"] != "387309->358946" {
		t.Fatalf("%+v", ev)
	}
	ev = parseLine("iter 5/30 seed=3 mode=disk-full kill_ms=51 acked=29 inflight=0 rejected=34 keys=360 open_ms=0 probe=full,freed=1540KiB,put-ok check=OK PASS")
	if ev.kind != evIter || ev.kv["rejected"] != "34" {
		t.Fatalf("%+v", ev)
	}
	ev = parseLine("iter 3/20 seed=3 mode=recovery-crash opens=[44ms:KILLED,21ms:KILLED] keys=200000 open_ms=75 check=OK PASS")
	if ev.kind != evIter || ev.kv["keys"] != "200000" {
		t.Fatalf("%+v", ev)
	}
}

func TestParseCases(t *testing.T) {
	cases := map[string]struct {
		name string
		pass bool
	}{
		"sweep CP-W05  n=3 [write]    reached     op 70574902607875 PRESENT tail=COMPLETE  check=OK PASS": {"sweep CP-W05 n=3", true},
		"sweep CP-C05  n=1 [rewrite]  reached     new image, put present tail=COMPLETE  check=OK PASS":    {"sweep CP-C05 n=1", true},
		"sweep CP-W03:2 [write] FAIL":                     {"sweep CP-W03 n=2", false},
		"fixture short-sentinel-3                   PASS": {"fixture short-sentinel-3", true},
		"fixture bad-crc                            FAIL": {"fixture bad-crc", false},
		"micro T6  PUT A; COMPACT                                       17 crash-point runs (15 distinct points), 20 random kills  PASS": {"micro T6", true},
		"micro T3 PUT A; DEL A FAIL at CP-W05:2": {"micro T3", false},
	}
	for line, want := range cases {
		ev := parseLine(line)
		if ev.kind != evCase || ev.name != want.name || ev.pass != want.pass {
			t.Errorf("%q -> %+v, want %v", line, ev, want)
		}
	}
}

func TestParseResultsAndNoise(t *testing.T) {
	if ev := parseLine("RESULT PASS 500/500 (seed=2, mode=default, 4m9s)"); ev.kind != evResult || !ev.pass {
		t.Fatalf("%+v", ev)
	}
	if ev := parseLine("RESULT FAIL 2 of 31 fixtures"); ev.kind != evResult || ev.pass {
		t.Fatalf("%+v", ev)
	}
	if ev := parseLine("COVERAGE PASS: all 24 transitions were exercised"); ev.kind != evCoverage || !ev.pass {
		t.Fatalf("%+v", ev)
	}
	if ev := parseLine("FAIL iter 17/500 seed=12345 mode=default kill_ms=134"); ev.kind != evFail {
		t.Fatalf("%+v", ev)
	}
	for _, noise := range []string{"warm-up: 401 keys in the shared database", "NOTE workdir is on tmpfs", "workdir kept: /x", "setup: building 200000 keys", ""} {
		if ev := parseLine(noise); ev.kind != evLog {
			t.Errorf("%q should be a plain log line, got kind %d", noise, ev.kind)
		}
	}
}
