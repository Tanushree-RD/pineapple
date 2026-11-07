package envshell

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// The live data viewer runs as its own detached process, so it keeps showing the database while you switch from
// the leapfrog environment to the hydra environment and while HYDRA kills the database. A small state file in the
// user cache directory remembers which process serves which port.

type liveState struct {
	PID int    `json:"pid"`
	DB  string `json:"db"`
}

func stateFile(port int) string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "hydra-ui")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "live-"+strconv.Itoa(port)+".json")
}

func readState(port int) (liveState, bool) {
	b, err := os.ReadFile(stateFile(port))
	if err != nil {
		return liveState{}, false
	}
	var s liveState
	if json.Unmarshal(b, &s) != nil || s.PID == 0 {
		return liveState{}, false
	}
	return s, true
}

// LiveURL is the address of the viewer on a port.
func LiveURL(port int) string { return "http://localhost:" + strconv.Itoa(port) }

// LiveRunning reports whether a viewer answers on the port, and which database it shows.
func LiveRunning(port int) (db string, ok bool) {
	c := http.Client{Timeout: 700 * time.Millisecond}
	resp, err := c.Get(LiveURL(port) + "/api/ping")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var p struct {
		DB string `json:"db"`
	}
	if json.NewDecoder(resp.Body).Decode(&p) != nil {
		return "", false
	}
	return p.DB, true
}

// StartLive starts (or reuses) the viewer for db on port and returns its URL. leapfrogBin is the program that
// serves it (`leapfrog live`).
func StartLive(leapfrogBin, db string, port int) (string, error) {
	abs, err := filepath.Abs(db)
	if err != nil {
		return "", err
	}
	if _, ok := LiveRunning(port); ok {
		return LiveURL(port), nil // the viewer follows the active-work pointer; its own database is only the fallback
	}
	logf, _ := os.OpenFile(stateFile(port)+".log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	cmd := exec.Command(leapfrogBin, "live", abs, "--port", strconv.Itoa(port), "--quiet")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach: survives the environment that started it
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("cannot start the live viewer: %w", err)
	}
	b, _ := json.Marshal(liveState{PID: cmd.Process.Pid, DB: abs})
	_ = os.WriteFile(stateFile(port), b, 0o644)
	go func() { _ = cmd.Wait() }() // reap it if it exits while we are alive
	for i := 0; i < 40; i++ {
		if _, ok := LiveRunning(port); ok {
			return LiveURL(port), nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("the live viewer did not start on port %d (is the port busy? see %s)", port, stateFile(port)+".log")
}

// StopLive stops the viewer on a port, if this tool started one.
func StopLive(port int) bool {
	s, ok := readState(port)
	if !ok {
		return false
	}
	_ = syscall.Kill(s.PID, syscall.SIGTERM)
	_ = os.Remove(stateFile(port)) // the small state file this tool created for the port
	return true
}

// The viewer shows whatever is actually being worked on. Whoever starts work writes a small pointer file: a database
// (what the user is typing commands against) or a directory (a torture run: the newest .db file under it is shown).

type activeTarget struct {
	DB    string `json:"db,omitempty"`
	Dir   string `json:"dir,omitempty"`
	Label string `json:"label,omitempty"`
}

func activeFile() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "hydra-ui")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "active.json")
}

// SetActiveDB points the viewer at a database the user is working on.
func SetActiveDB(db string) {
	abs, err := filepath.Abs(db)
	if err != nil {
		return
	}
	b, _ := json.Marshal(activeTarget{DB: abs, Label: "your database"})
	_ = os.WriteFile(activeFile(), b, 0o644)
}

// SetActiveRun points the viewer at a torture run's working directory.
func SetActiveRun(dir, label string) {
	b, _ := json.Marshal(activeTarget{Dir: dir, Label: label})
	_ = os.WriteFile(activeFile(), b, 0o644)
}

// ClearActive removes the pointer (the viewer then shows nothing until work starts).
func ClearActive() { _ = os.Remove(activeFile()) }

// ResolveActive returns the file the viewer should read now and a label for it. fallback is used when no pointer
// exists. For a torture run it is the most recently modified .db file under the run directory (three levels deep).
func ResolveActive(fallback string) (path, label string) {
	b, err := os.ReadFile(activeFile())
	if err != nil {
		return fallback, "your database"
	}
	var a activeTarget
	if json.Unmarshal(b, &a) != nil {
		return fallback, "your database"
	}
	if a.DB != "" {
		return a.DB, a.Label
	}
	if a.Dir == "" {
		return fallback, "your database"
	}
	var best string
	var bestT time.Time
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				if depth < 3 {
					walk(p, depth+1)
				}
				continue
			}
			if filepath.Ext(e.Name()) == ".db" {
				if info, err := e.Info(); err == nil && info.ModTime().After(bestT) {
					best, bestT = p, info.ModTime()
				}
			}
		}
	}
	walk(a.Dir, 0)
	if best == "" {
		return filepath.Join(a.Dir, "database.db"), a.Label
	}
	return best, a.Label
}

// Verdict is the highlighted banner the live page shows after the `failure` / `success` demos: what was promised
// (ACKed), what is missing, and whether HYDRA caught it.
type Verdict struct {
	Kind    string   `json:"kind"` // "fail" or "pass"
	Title   string   `json:"title"`
	Lines   []string `json:"lines,omitempty"`
	Missing []string `json:"missing,omitempty"` // things that were acknowledged (or promised) but are not there
}

func verdictFile() string { return filepath.Join(filepath.Dir(activeFile()), "verdict.json") }

// SetVerdict makes the page show a banner until ClearVerdict (or the next command).
func SetVerdict(v Verdict) {
	b, _ := json.Marshal(v)
	_ = os.WriteFile(verdictFile(), b, 0o644)
}

// ClearVerdict removes the banner.
func ClearVerdict() { _ = os.Remove(verdictFile()) }

// ReadVerdict returns the current banner, or nil.
func ReadVerdict() *Verdict {
	b, err := os.ReadFile(verdictFile())
	if err != nil {
		return nil
	}
	var v Verdict
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	return &v
}
