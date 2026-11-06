package main

// Parsing of bin/torture's output into events. The control room never reimplements HYDRA: it runs
// the real tool and renders what the tool prints, so what you see is exactly what HYDRA decided.

import (
	"regexp"
	"strconv"
	"strings"
)

type eventKind int

const (
	evLog      eventKind = iota // anything else (setup, notes)
	evIter                      // iter 17/500 ... PASS
	evCase                      // sweep / fixture / micro line with a verdict
	evFail                      // the first line of a failure block
	evResult                    // RESULT PASS|FAIL ...
	evCoverage                  // COVERAGE PASS|FAIL ...
)

type event struct {
	kind    eventKind
	line    string
	pass    bool
	i, n    int               // evIter: iteration and total
	kv      map[string]string // evIter: kill_ms, acked, inflight, rejected, keys, open_ms and any extras
	name    string            // evCase: "sweep CP-W05 n=1", "fixture torn-header", "micro T3"
	verdict string            // evResult / evCoverage: the whole line
}

var (
	iterRE    = regexp.MustCompile(`^iter (\d+)/(\d+) (.*?)\s*check=OK PASS$`)
	sweepRE   = regexp.MustCompile(`^sweep (CP-[A-Z]\d+)\s+n=(\d+)\s+\[([^\]]+)\]`)
	sweepFail = regexp.MustCompile(`^sweep (CP-[A-Z]\d+):(\d+) \[([^\]]+)\] FAIL`)
	fixtureRE = regexp.MustCompile(`^fixture (\S+)\s+(PASS|FAIL)`)
	microRE   = regexp.MustCompile(`^micro (T\d+)\s.*\b(PASS|FAIL)\b`)
	microFail = regexp.MustCompile(`^micro (T\d+) .* FAIL at`)
)

func parseKV(s string) map[string]string {
	m := map[string]string{}
	for _, tok := range strings.Fields(s) {
		if i := strings.IndexByte(tok, '='); i > 0 {
			m[tok[:i]] = tok[i+1:]
		}
	}
	return m
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// parseLine classifies one line of torture output.
func parseLine(line string) event {
	line = strings.TrimRight(line, "\r\n")
	ev := event{kind: evLog, line: line}
	switch {
	case strings.HasPrefix(line, "iter "):
		if m := iterRE.FindStringSubmatch(line); m != nil {
			ev.kind, ev.pass = evIter, true
			ev.i, ev.n = atoi(m[1]), atoi(m[2])
			ev.kv = parseKV(m[3])
		}
	case strings.HasPrefix(line, "FAIL "):
		ev.kind = evFail
	case strings.HasPrefix(line, "RESULT "):
		ev.kind, ev.pass, ev.verdict = evResult, strings.HasPrefix(line, "RESULT PASS"), line
	case strings.HasPrefix(line, "COVERAGE "):
		ev.kind, ev.pass, ev.verdict = evCoverage, strings.HasPrefix(line, "COVERAGE PASS"), line
	case strings.HasPrefix(line, "sweep "):
		if m := sweepFail.FindStringSubmatch(line); m != nil {
			ev.kind, ev.name = evCase, "sweep "+m[1]+" n="+m[2]
		} else if m := sweepRE.FindStringSubmatch(line); m != nil {
			ev.kind, ev.pass, ev.name = evCase, strings.HasSuffix(strings.TrimSpace(line), "PASS"), "sweep "+m[1]+" n="+m[2]
		}
	case strings.HasPrefix(line, "fixture "):
		if m := fixtureRE.FindStringSubmatch(line); m != nil {
			ev.kind, ev.pass, ev.name = evCase, m[2] == "PASS", "fixture "+m[1]
		}
	case strings.HasPrefix(line, "micro "):
		if m := microFail.FindStringSubmatch(line); m != nil {
			ev.kind, ev.name = evCase, "micro "+m[1]
		} else if m := microRE.FindStringSubmatch(line); m != nil {
			ev.kind, ev.pass, ev.name = evCase, m[2] == "PASS", "micro "+m[1]
		}
	}
	return ev
}
