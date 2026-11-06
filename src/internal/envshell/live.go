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
	if cur, ok := LiveRunning(port); ok {
		if cur == abs {
			return LiveURL(port), nil
		}
		StopLive(port) // another database was on this port: replace the viewer
		time.Sleep(300 * time.Millisecond)
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
