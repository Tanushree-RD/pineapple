package engine

// Crash-point hooks (SPEC 12, 15.6). Every named CP-xxx in the write, recovery, compaction
// and superblock paths calls Hit("CP-xxx"). Both hooks are no-ops unless the environment
// asks for a crash:
//
//	KVDB_CRASH=CP-W03:7   SIGKILL this process on the 7th hit of CP-W03
//	KVDB_CRASH_SEED=n     seed for the torn-write prefix length (default 1)
//
// At a write point (CP-W03, CP-C02) the process first writes a seeded random PREFIX of the
// chunk it was about to write, so the file really contains a torn frame, and only then dies.
// HYDRA's default mode never sets these variables: it stays purely random-timer.

import (
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type crashTarget struct {
	name string
	n    int64
	seed uint64
}

var (
	crashT    = parseCrashEnv()
	crashHits atomic.Int64
)

func parseCrashEnv() crashTarget {
	v := os.Getenv("KVDB_CRASH")
	if v == "" {
		return crashTarget{}
	}
	name, num, found := strings.Cut(v, ":")
	n := int64(1)
	if found {
		if x, err := strconv.ParseInt(num, 10, 64); err == nil && x > 0 {
			n = x
		}
	}
	seed := uint64(1)
	if s := os.Getenv("KVDB_CRASH_SEED"); s != "" {
		if x, err := strconv.ParseUint(s, 10, 64); err == nil {
			seed = x
		}
	}
	return crashTarget{name: name, n: n, seed: seed}
}

// Hit marks crash point name. When KVDB_CRASH names it and this is the Nth hit, the process
// kills itself. Exported so serve can mark CP-W07 right after an ACK line is flushed.
func Hit(name string) {
	if crashT.name == "" || name != crashT.name {
		return
	}
	if crashHits.Add(1) == crashT.n {
		die()
	}
}

// hitWrite is Hit for a point that sits in front of a write of p to w. On the fatal hit it
// writes a seeded random prefix of p first, so the kill leaves a real torn write.
func hitWrite(name string, w io.Writer, p []byte) {
	if crashT.name == "" || name != crashT.name {
		return
	}
	n := crashHits.Add(1)
	if n != crashT.n {
		return
	}
	if k := int(splitmix64(crashT.seed+uint64(n)) % uint64(len(p)+1)); k > 0 {
		w.Write(p[:k])
	}
	die()
}

func die() {
	if p, err := os.FindProcess(os.Getpid()); err == nil {
		p.Kill() // SIGKILL on Unix
	}
	for {
		time.Sleep(time.Hour)
	}
}

func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}
