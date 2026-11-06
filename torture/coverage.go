package main

// Behavior coverage (SPEC 15.9, `--coverage`): which persistence transitions were actually exercised
// by a kill. The sweep, micro and fixtures modes each leave a failures/coverage-<mode>.json behind;
// this reads them and prints one row per transition with its hit count. A transition with zero hits
// is a gap: it must be fixed or explained in BREAKING.md.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type covRow struct {
	behavior, transition string
	hits                 int
	sources              map[string]int
}

func runCoverage(cfg *Config) int {
	dir := "failures"
	var sweep struct {
		Sweep []sweepResult `json:"sweep"`
	}
	var micro []microResult
	var fixtures []string
	have := map[string]bool{}
	load := func(name string, v any) {
		b, err := os.ReadFile(filepath.Join(dir, "coverage-"+name+".json"))
		if err != nil {
			return
		}
		if json.Unmarshal(b, v) == nil {
			have[name] = true
		}
	}
	load("sweep", &sweep)
	load("micro", &micro)
	load("fixtures", &fixtures)
	if len(have) == 0 {
		fmt.Fprintln(os.Stderr, "torture: no coverage data in ./failures. Run `--mode sweep`, `--mode micro` and `--mode fixtures` first (from this directory).")
		return 2
	}
	for _, m := range []string{"sweep", "micro", "fixtures"} {
		if !have[m] {
			fmt.Printf("NOTE no coverage-%s.json: rows that depend on it will show as gaps\n", m)
		}
	}

	var rows []*covRow
	add := func(behavior, transition string) *covRow {
		r := &covRow{behavior: behavior, transition: transition, sources: map[string]int{}}
		rows = append(rows, r)
		return r
	}
	hit := func(r *covRow, source string) { r.hits++; r.sources[source]++ }

	// Index the data.
	type key struct{ scenario, cp string }
	sweepHits := map[key]int{}
	for _, s := range sweep.Sweep {
		if s.Reached {
			sweepHits[key{s.Scenario, s.CP}]++
		}
	}
	microHits := map[string]map[string]int{} // cp -> trace -> hits
	for _, m := range micro {
		if m.Reached && m.CP != "" {
			if microHits[m.CP] == nil {
				microHits[m.CP] = map[string]int{}
			}
			microHits[m.CP][m.Trace]++
		}
	}
	fxPassed := map[string]bool{}
	for _, f := range fixtures {
		fxPassed[f] = true
	}

	// W: the normal append, W0 -> W1 -> W2 -> W3 -> W4 -> W5.
	wDesc := map[string]string{
		"CP-W01": "W0 admitted, nothing written", "CP-W03": "W1 frame being streamed (torn write)",
		"CP-W04": "W2 whole frame, fsync pending", "CP-W05": "W3 fsync returned (durable)",
		"CP-W06": "W4 published, ACK pending", "CP-W07": "W5 ACK flushed",
	}
	for _, cp := range []string{"CP-W01", "CP-W03", "CP-W04", "CP-W05", "CP-W06", "CP-W07"} {
		r := add("W normal append", cp+" "+wDesc[cp])
		hit2 := sweepHits[key{"write", cp}]
		for i := 0; i < hit2; i++ {
			hit(r, "sweep:write")
		}
		for tr, n := range microHits[cp] {
			for i := 0; i < n; i++ {
				hit(r, "micro:"+tr)
			}
		}
	}
	// R: recovery.
	for _, cp := range []string{"CP-R01", "CP-R02", "CP-R03"} {
		desc := map[string]string{"CP-R01": "R1 superblock chosen", "CP-R02": "R2 checkpoint loaded or fallback chosen", "CP-R03": "R3 tail scanned, index built"}[cp]
		r := add("R recovery", cp+" "+desc)
		for i := 0; i < sweepHits[key{"recover", cp}]; i++ {
			hit(r, "sweep:recover")
		}
		for tr, n := range microHits[cp] {
			for i := 0; i < n; i++ {
				hit(r, "micro:"+tr)
			}
		}
	}
	// C: compaction (the explicit COMPACT command).
	cDesc := map[string]string{"CP-C01": "C1 temp created", "CP-C02": "C1 temp being written", "CP-C03": "C2 temp fsynced", "CP-C04": "C2 before rename", "CP-C05": "C3 renamed, swap pending", "CP-C06": "C4 handle and index swapped"}
	for _, cp := range []string{"CP-C01", "CP-C02", "CP-C03", "CP-C04", "CP-C05", "CP-C06"} {
		r := add("C compaction", cp+" "+cDesc[cp])
		for i := 0; i < sweepHits[key{"compact", cp}]; i++ {
			hit(r, "sweep:compact")
		}
		for _, tr := range []string{"T6", "T7"} {
			for i := 0; i < microHits[cp][tr]; i++ {
				hit(r, "micro:"+tr)
			}
		}
	}
	// S: superblock updates.
	r := add("S checkpoint / config", "CP-S01 S1 inactive slot written for a CHECKPOINT")
	for i := 0; i < sweepHits[key{"checkpoint", "CP-S01"}]; i++ {
		hit(r, "sweep:checkpoint")
	}
	for i := 0; i < microHits["CP-S01"]["T8"]; i++ {
		hit(r, "micro:T8")
	}
	r = add("S checkpoint / config", "CP-S01 S1 inactive slot written for a CAP CHANGE")
	for i := 0; i < sweepHits[key{"cap-change", "CP-S01"}]; i++ {
		hit(r, "sweep:cap-change")
	}
	for i := 0; i < microHits["CP-S01"]["T10"]; i++ {
		hit(r, "micro:T10")
	}
	// F4: tail repair. Detect a short sentinel -> restore -> fsync -> append.
	r = add("F4 tail repair", "short sentinel detected, repair starts (CP-W02 before)")
	for i := 0; i < sweepHits[key{"repair-short", "CP-W02"}]; i++ {
		hit(r, "sweep:repair-short")
	}
	for i := 0; i < microHits["CP-W02"]["T9"]; i++ {
		hit(r, "micro:T9")
	}
	r = add("F4 tail repair", "end_magic restored, fsync pending (CP-W02 after)")
	for _, s := range sweep.Sweep {
		if s.Reached && s.Scenario == "repair-short" && s.CP == "CP-W02" && s.N == 2 {
			hit(r, "sweep:repair-short")
		}
	}
	r = add("F4 tail repair", "repaired tail then APPEND works (fixtures)")
	for _, f := range []string{"short-sentinel-then-put", "torn-then-put", "stale-frame-then-put"} {
		if fxPassed[f] {
			hit(r, "fixture:"+f)
		}
	}
	// E: disk-full rewrite. Admission fails -> replacement built -> durable -> swapped.
	for _, cp := range []string{"CP-C01", "CP-C03", "CP-C05", "CP-C06"} {
		desc := map[string]string{"CP-C01": "admission fails, replacement being built", "CP-C03": "replacement complete and fsynced", "CP-C05": "replacement renamed durable", "CP-C06": "swapped in memory"}[cp]
		r := add("E disk-full rewrite", cp+" "+desc)
		for i := 0; i < sweepHits[key{"rewrite", cp}]; i++ {
			hit(r, "sweep:rewrite")
		}
		for i := 0; i < microHits[cp]["T11"]; i++ {
			hit(r, "micro:T11")
		}
	}

	fmt.Printf("%-24s %-58s %5s  %s\n", "BEHAVIOR", "TRANSITION", "HITS", "SOURCES")
	gaps := 0
	for _, r := range rows {
		var src []string
		for s, n := range r.sources {
			src = append(src, fmt.Sprintf("%s x%d", s, n))
		}
		sort.Strings(src)
		line := strings.Join(src, ", ")
		if r.hits == 0 {
			gaps++
			line = "GAP"
		}
		fmt.Printf("%-24s %-58s %5d  %s\n", r.behavior, r.transition, r.hits, truncate(line, 70))
	}
	if gaps > 0 {
		fmt.Printf("COVERAGE FAIL: %d transitions have zero hits. Fix them or explain them in BREAKING.md (SPEC 15.9).\n", gaps)
		return 1
	}
	fmt.Printf("COVERAGE PASS: all %d transitions were exercised by a kill or a crafted crash image.\n", len(rows))
	return 0
}
