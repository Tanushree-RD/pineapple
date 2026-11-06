package main

// One serve session: spawn `kvdb serve --b64`, drive concurrent lanes, read replies, SIGKILL,
// then read stdout to EOF (SPEC 14.5: ACK lines that were flushed before the kill are still in
// the pipe and must be counted; a partial last line is ignored).

import (
	"bufio"
	"bytes"
	"fmt"
	"hash"
	"hash/crc64"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	pipelineDepth = 8                // requests a lane keeps in flight (MASTER_PROMPT H1)
	readyTimeout  = 60 * time.Second // generous: recovery of a big file can take a while
)

// ExitInfo describes how serve ended.
type ExitInfo struct {
	EarlyExit bool // serve ended on its own before we killed it
	Signaled  bool
	Signal    syscall.Signal
	Code      int
}

func (e ExitInfo) String() string {
	switch {
	case e.Signaled:
		return "killed by " + e.Signal.String()
	default:
		return "exit code " + strconv.Itoa(e.Code)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Session is one running `kvdb serve --b64` process.
type Session struct {
	oracle *Oracle
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *lockedBuffer

	writeMu    sync.Mutex // a request (a whole BATCH, header and op lines) is written under it
	sems       []chan struct{}
	ready2send []chan prepared

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup // lanes

	readerDone chan struct{}
	ready      chan string

	mu        sync.Mutex
	anomalies []string
	replies   int
}

// StartSession spawns serve. extra holds additional serve flags such as "--max-bytes", "N".
func StartSession(kvdb, dbPath string, oracle *Oracle, lanes int, extra ...string) (*Session, error) {
	return StartSessionEnv(kvdb, dbPath, oracle, lanes, nil, extra...)
}

// StartSessionEnv is StartSession with extra environment variables (KVDB_CRASH, KVDB_CRASH_SEED).
func StartSessionEnv(kvdb, dbPath string, oracle *Oracle, lanes int, env []string, extra ...string) (*Session, error) {
	args := append([]string{"serve", dbPath, "--b64"}, extra...)
	cmd := exec.Command(kvdb, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	s := &Session{
		oracle:     oracle,
		cmd:        cmd,
		stderr:     &lockedBuffer{},
		stop:       make(chan struct{}),
		readerDone: make(chan struct{}),
		ready:      make(chan string, 1),
	}
	cmd.Stderr = s.stderr
	var err error
	if s.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot start %s: %w", kvdb, err)
	}
	s.sems = make([]chan struct{}, lanes)
	for i := range s.sems {
		s.sems[i] = make(chan struct{}, pipelineDepth)
	}
	go s.readReplies(stdout)
	return s, nil
}

func (s *Session) anomaly(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.anomalies) < 20 {
		s.anomalies = append(s.anomalies, fmt.Sprintf(format, args...))
	}
}

// Anomalies are protocol violations seen on stdout (unparseable line, unknown id, ...).
func (s *Session) Anomalies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.anomalies...)
}

// Dead reports whether serve has closed its stdout, which means the process is gone.
func (s *Session) Dead() bool {
	select {
	case <-s.readerDone:
		return true
	default:
		return false
	}
}

// Stderr returns what serve wrote to stderr.
func (s *Session) Stderr() string { return s.stderr.String() }

// readReplies is the single reply reader. Only COMPLETE lines count (SPEC 14.2); the reader assigns
// ack_seq in arrival order through oracle.Ack, which is the commit order.
func (s *Session) readReplies(r io.Reader) {
	defer close(s.readerDone)
	br := bufio.NewReaderSize(r, 1<<20)
	first := true
	for {
		line, err := br.ReadString('\n')
		if strings.HasSuffix(line, "\n") {
			s.handleLine(strings.TrimSuffix(line, "\n"), &first)
		}
		if err != nil {
			return // EOF: a trailing partial line is dropped on purpose
		}
	}
}

func (s *Session) handleLine(line string, first *bool) {
	f := strings.Fields(line)
	if *first {
		*first = false
		if len(f) > 0 && f[0] == "READY" {
			s.ready <- line
			return
		}
		s.anomaly("first line was %q, expected READY", truncate(line, 80))
		close(s.ready)
		return
	}
	if len(f) == 0 {
		s.anomaly("empty line on stdout")
		return
	}
	switch f[0] {
	case "ACK":
		id, ok := parseID(f, 2)
		if !ok {
			s.anomaly("malformed ACK line %q", truncate(line, 80))
			return
		}
		if err := s.oracle.Ack(id); err != nil {
			s.anomaly("%v", err)
			return
		}
		s.replied(id)
	case "ERR":
		id, ok := parseID(f, 3)
		if !ok {
			s.anomaly("malformed ERR line %q", truncate(line, 80))
			return
		}
		var err error
		switch f[2] {
		case "FULL", "INVALID":
			err = s.oracle.Reject(id, f[2])
		case "IO":
			err = s.oracle.Unknown(id)
		default:
			s.anomaly("unknown ERR reason in %q", truncate(line, 80))
			return
		}
		if err != nil {
			s.anomaly("%v", err)
			return
		}
		s.replied(id)
	default:
		s.anomaly("unexpected line on stdout: %q", truncate(line, 80))
	}
}

func parseID(f []string, wantFields int) (uint64, bool) {
	if len(f) != wantFields {
		return 0, false
	}
	id, err := strconv.ParseUint(f[1], 10, 64)
	return id, err == nil && id > 0
}

// replied frees one pipeline slot of the op's lane.
func (s *Session) replied(id uint64) {
	s.mu.Lock()
	s.replies++
	s.mu.Unlock()
	op, ok := s.oracle.Op(id)
	if !ok || op.Lane < 0 || op.Lane >= len(s.sems) {
		return
	}
	select {
	case <-s.sems[op.Lane]:
	default:
	}
}

// Replies is the number of ACK/ERR lines read so far.
func (s *Session) Replies() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replies
}

// WaitReady blocks until serve prints READY (open and recovery finished) or dies.
func (s *Session) WaitReady() (string, error) {
	select {
	case line, ok := <-s.ready:
		if !ok {
			return "", fmt.Errorf("serve did not start with READY: %s", strings.Join(s.Anomalies(), "; "))
		}
		return line, nil
	case <-s.readerDone:
		return "", fmt.Errorf("serve exited before printing READY (open or recovery failed); stderr: %s", truncate(strings.TrimSpace(s.Stderr()), 400))
	case <-time.After(readyTimeout):
		return "", fmt.Errorf("serve did not print READY within %s", readyTimeout)
	}
}

// send writes one whole request atomically with respect to the other lanes.
func (s *Session) send(req []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.stdin.Write(req)
	return err
}

// prepared is a request that has been generated and encoded but not yet sent.
type prepared struct {
	op  *Op
	req []byte
}

// PrepareLanes starts one producer per generator that builds the next request ahead of time. Call
// it BEFORE WaitReady so a 70 MiB batch is already encoded when serve says READY and the kill
// timer starts. The producers only generate; nothing is recorded or sent until StartLanes.
func (s *Session) PrepareLanes(gens []*Generator) {
	s.ready2send = make([]chan prepared, len(gens))
	for lane, g := range gens {
		ch := make(chan prepared, 1)
		s.ready2send[lane] = ch
		go func(g *Generator, ch chan<- prepared) {
			for {
				op := g.Next()
				p := prepared{op: op, req: EncodeRequest(op)}
				select {
				case ch <- p:
				case <-s.stop:
					return
				}
			}
		}(g, ch)
	}
}

// StartLanes launches one sender per prepared lane. Each lane keeps up to pipelineDepth requests in
// flight and records every request in the oracle BEFORE writing it (SPEC 14.2).
func (s *Session) StartLanes() {
	for lane := range s.ready2send {
		s.wg.Add(1)
		go func(lane int, ch <-chan prepared) {
			defer s.wg.Done()
			for {
				select {
				case s.sems[lane] <- struct{}{}:
				case <-s.stop:
					return
				}
				var p prepared
				select {
				case p = <-ch:
				case <-s.stop:
					return
				}
				select {
				case <-s.stop:
					return
				default:
				}
				s.oracle.Sent(p.op)
				if err := s.send(p.req); err != nil {
					return // serve is gone; the op stays IN_FLIGHT, which is correct
				}
			}
		}(lane, s.ready2send[lane])
	}
}

// SendNow records and sends one extra request outside the lanes (used for COMPACT).
func (s *Session) SendNow(op *Op) error {
	s.oracle.Sent(op)
	return s.send(EncodeRequest(op))
}

// StopLanes stops the lanes and producers but leaves serve running, so in-flight requests finish.
func (s *Session) StopLanes() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
}

// KillAndDrain SIGKILLs serve, stops the lanes, reads stdout to EOF and reaps the process.
func (s *Session) KillAndDrain() ExitInfo {
	var info ExitInfo
	select {
	case <-s.readerDone:
		info.EarlyExit = true
	default:
	}
	s.stopOnce.Do(func() { close(s.stop) })
	_ = s.cmd.Process.Signal(syscall.SIGKILL)
	s.wg.Wait() // lanes: a blocked write returns EPIPE once the process is dead
	<-s.readerDone
	_ = s.stdin.Close()
	err := s.cmd.Wait()
	if ps := s.cmd.ProcessState; ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			info.Signaled, info.Signal = true, ws.Signal()
		} else {
			info.Code = ps.ExitCode()
		}
	}
	_ = err
	return info
}

// Close is a best-effort cleanup for sessions abandoned on an error path.
func (s *Session) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.wg.Wait()
	<-s.readerDone
	_ = s.stdin.Close()
	_ = s.cmd.Wait()
}

// runKVDB runs a one-shot kvdb command and returns stdout, stderr and the exit code.
func runKVDB(kvdb string, timeout time.Duration, args ...string) (stdout, stderr string, code int, err error) {
	cmd := exec.Command(kvdb, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Start(); err != nil {
		return "", "", -1, fmt.Errorf("cannot start %s: %w", kvdb, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case werr := <-done:
		code = 0
		if werr != nil {
			if ee, ok := werr.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				return so.String(), se.String(), -1, werr
			}
		}
		return so.String(), se.String(), code, nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return so.String(), se.String(), -1, fmt.Errorf("kvdb %s timed out after %s", strings.Join(args, " "), timeout)
	}
}

// fileDigest returns a CRC64 of the whole file, used to prove that recovery writes no bytes.
func fileDigest(path string) (uint64, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	h := newCRC64()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, 0, err
	}
	return h.Sum64(), n, nil
}

func newCRC64() hash.Hash64 { return crc64.New(crc64.MakeTable(crc64.ECMA)) }
