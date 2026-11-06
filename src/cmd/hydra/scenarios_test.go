package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chethan-9817/pineapple/src/internal/tui"
)

var wantIDs = strings.Fields("A1 A2 A3 A4 A5 A6 A7 B1 B2 B3 B4 B5 B6 C1 C2 C3 C4 C5 C6 D1 D2 D3 D4 E1 E2 E3 E4 E5 F1 F2 F3 F4")

func TestTheTableHasExactlyThe32ScenariosOfSpec16(t *testing.T) {
	got := scenarios()
	if len(got) != 32 {
		t.Fatalf("%d scenarios, SPEC 16 has 32", len(got))
	}
	for i, s := range got {
		if s.ID != wantIDs[i] {
			t.Fatalf("position %d is %s, want %s", i, s.ID, wantIDs[i])
		}
		if s.Title == "" || s.Group == "" || len(s.Steps) == 0 {
			t.Fatalf("%s is incomplete: %+v", s.ID, s)
		}
		if _, ok := findScenario(strings.ToLower(s.ID)); !ok {
			t.Fatalf("%s not found case-insensitively", s.ID)
		}
	}
}

func TestEveryStepIsRunnable(t *testing.T) {
	validModes := map[string]bool{"sweep": true, "fixtures": true, "micro": true, "compaction": true, "recovery-crash": true,
		"disk-full": true, "large-values": true, "large-batches": true}
	for _, s := range scenarios() {
		for _, st := range s.Steps {
			switch st.Kind {
			case "native":
				if _, ok := natives[st.Nat]; !ok {
					t.Errorf("%s: unknown native check %q", s.ID, st.Nat)
				}
			case "torture":
				for i, a := range st.Args {
					if a == "--mode" && !validModes[st.Args[i+1]] {
						t.Errorf("%s: unknown torture mode %q", s.ID, st.Args[i+1])
					}
				}
				if len(st.Args) == 0 || st.Total <= 0 {
					t.Errorf("%s: torture step %q needs args and a total", s.ID, st.Name)
				}
			case "gotest":
				if len(st.Args) < 4 || st.Args[0] != "test" {
					t.Errorf("%s: bad go test step %+v", s.ID, st)
				}
			default:
				t.Errorf("%s: unknown step kind %q", s.ID, st.Kind)
			}
		}
	}
}

func TestEveryModeInTheMenuHasDefaults(t *testing.T) {
	for _, m := range modeList {
		if m.name == "" || m.desc == "" || len(m.args) == 0 || m.total <= 0 {
			t.Errorf("incomplete mode %+v", m)
		}
		if got, ok := findMode(m.name); !ok || got.name != m.name {
			t.Errorf("findMode(%s)", m.name)
		}
	}
}

func TestStepKeysDedupe(t *testing.T) {
	a := tort("x", 6, "--mode", "fixtures")
	b := tort("other name, same command", 99, "--mode", "fixtures")
	if stepKey(a, false) != stepKey(b, false) {
		t.Fatal("the same command must share a cache key whatever its title")
	}
	if stepKey(tort("x", 1, "--iterations", "100"), true) == stepKey(tort("x", 1, "--iterations", "100"), false) {
		t.Fatal("quick and full runs must not share a key")
	}
}

func TestUsageAndScenarioListing(t *testing.T) {
	tui.SetColor(false)
	var out bytes.Buffer
	listScenarios(&out)
	for _, id := range wantIDs {
		if !strings.Contains(out.String(), " "+id+" ") {
			t.Errorf("scenario %s missing from the listing", id)
		}
	}
	out.Reset()
	usage(&out)
	for _, want := range []string{"hydra judge", "hydra scenario", "hydra all", "--quick", "hydra replay"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}

func buildTools(t *testing.T) *Env {
	t.Helper()
	tui.SetColor(false)
	dir := t.TempDir()
	for name, pkg := range map[string]string{"kvdb": "../kvdb", "torture": "../../../torture"} {
		if b, err := exec.Command("go", "build", "-o", filepath.Join(dir, name), pkg).CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", name, err, b)
		}
	}
	e := &Env{torture: filepath.Join(dir, "torture"), kvdb: filepath.Join(dir, "kvdb"), runDir: t.TempDir(), seed: "1", quiet: true}
	return e
}

// Every "by hand" check, against the real kvdb. Each statement it makes must hold.
func TestNativeChecksAgainstTheRealKVDB(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	e := buildTools(t)
	for name := range natives {
		name := name
		t.Run(name, func(t *testing.T) {
			checks := e.runNative(name)
			if len(checks) < 2 {
				t.Fatalf("%s made only %d checks", name, len(checks))
			}
			for _, c := range checks {
				if !c.OK {
					t.Errorf("%s: FAILED: %s (%s)", name, c.Desc, c.Detail)
				}
			}
		})
	}
}

// A whole job through the real torture: spawn, parse, dashboard, verdict.
func TestRunJobEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	e := buildTools(t)
	e.quiet = false // the full (non-compact) view lists every case
	var out bytes.Buffer
	r := e.runJob(&out, Job{Name: "fixtures", Args: []string{"--mode", "fixtures"}, Total: 31})
	if !r.OK || r.Passed != 31 || r.Failed != 0 || !strings.Contains(r.Verdict, "RESULT PASS 31/31") {
		t.Fatalf("%+v\n%s", r, out.String())
	}
	if !strings.Contains(out.String(), "fixture torn-header") {
		t.Fatalf("plain output should list the cases:\n%s", out.String())
	}
	// A job that must fail is reported as a failure, with what HYDRA said.
	bad := e.runJob(&out, Job{Name: "bad flag", Args: []string{"--mode", "no-such-mode", "--seed", "1"}, Total: 1})
	if bad.OK {
		t.Fatal("an invalid mode must not be reported as a pass")
	}
	// A scenario made of real steps.
	sc, _ := findScenario("B5")
	res := e.runScenario(&out, sc, nil)
	if !res.OK && !allSkipped(res) {
		t.Fatalf("B5 failed:\n%s", out.String())
	}
}

func allSkipped(r ScenarioResult) bool {
	for _, s := range r.Steps {
		if !s.OK {
			return false
		}
	}
	return true
}
