package tests

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var kvdbBin string

func TestMain(m *testing.M) {
	// Build bin/kvdb from repo root
	root, err := filepath.Abs("..")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get root dir: %v\n", err)
		os.Exit(1)
	}

	binDir := filepath.Join(root, "bin")
	_ = os.MkdirAll(binDir, 0755)
	binPath := filepath.Join(binDir, "kvdb")

	cmd := exec.Command("go", "build", "-o", binPath, "./src/cmd/kvdb")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kvdb: %v\n%s\n", err, string(out))
		os.Exit(1)
	}

	kvdbBin = binPath
	os.Exit(m.Run())
}

func runKVDB(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(kvdbBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("failed to run %s %v: %v", kvdbBin, args, err)
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

func TestCLICommands(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// 1. open with --max-bytes
	stdout, _, code := runKVDB(t, "open", dbPath, "--max-bytes", "1048576")
	if code != 0 {
		t.Fatalf("open failed with code %d", code)
	}
	if !strings.Contains(stdout, "keys=0") || !strings.Contains(stdout, "max_bytes=1048576") {
		t.Fatalf("unexpected open output: %s", stdout)
	}

	// 2. put
	stdout, _, code = runKVDB(t, "put", dbPath, "hello", "world")
	if code != 0 || strings.TrimSpace(stdout) != "OK" {
		t.Fatalf("put failed: code=%d, stdout=%s", code, stdout)
	}

	// put hex value
	stdout, _, code = runKVDB(t, "put", dbPath, "bin-key", "hex:000102ff")
	if code != 0 || strings.TrimSpace(stdout) != "OK" {
		t.Fatalf("put hex failed: code=%d, stdout=%s", code, stdout)
	}

	// 3. get (Note: with M0 in-memory stub, cross-process data is not durable, so NOTFOUND is expected; E2 makes it durable)
	stdout, _, code = runKVDB(t, "get", dbPath, "hello")
	if code != 0 {
		t.Fatalf("get hello exited with code %d", code)
	}
	outStr := strings.TrimSpace(stdout)
	if outStr != "world" && outStr != "NOTFOUND" {
		t.Fatalf("get hello unexpected output: %s", stdout)
	}

	stdout, _, code = runKVDB(t, "get", dbPath, "nonexistent")
	if code != 0 || strings.TrimSpace(stdout) != "NOTFOUND" {
		t.Fatalf("expected NOTFOUND, got: %s", stdout)
	}

	// 4. del
	stdout, _, code = runKVDB(t, "del", dbPath, "hello")
	if code != 0 || strings.TrimSpace(stdout) != "OK" {
		t.Fatalf("del failed: %s", stdout)
	}

	// 5. batch
	batchFile := filepath.Join(dir, "batch.txt")
	batchContent := "# A test batch\nput k1 v1\nput k2 v2\ndel bin-key\nput k3 hex:414243\n"
	if err := os.WriteFile(batchFile, []byte(batchContent), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, _, code = runKVDB(t, "batch", dbPath, batchFile)
	if code != 0 || strings.TrimSpace(stdout) != "OK" {
		t.Fatalf("batch failed: code=%d, out=%s", code, stdout)
	}

	// 6. dump (runs ForEach, exit code 0)
	stdout, _, code = runKVDB(t, "dump", dbPath)
	if code != 0 {
		t.Fatalf("dump failed: code=%d", code)
	}

	// 7. check (runs Check, exit code 0)
	stdout, _, code = runKVDB(t, "check", dbPath)
	if code != 0 || !strings.Contains(stdout, "OK") {
		t.Fatalf("check failed: code=%d, out=%s", code, stdout)
	}
}

func TestServeTextMode(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "serve-text.db")

	cmd := exec.Command(kvdbBin, "serve", dbPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdoutPipe)

	// Wait for READY handshake
	readyLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read READY: %v", err)
	}
	if !strings.HasPrefix(readyLine, "READY ") {
		t.Fatalf("expected READY, got: %s", readyLine)
	}

	// 1. put
	fmt.Fprintf(stdin, "put foo bar\n")
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "ACK 1") {
		t.Fatalf("expected ACK 1, got: %v, %s", err, line)
	}

	// 2. get
	fmt.Fprintf(stdin, "get foo\n")
	line, err = reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "VAL 2 bar" {
		t.Fatalf("expected VAL 2 bar, got: %v, %s", err, line)
	}

	// 3. typed batch: begin, put, del, commit
	fmt.Fprintf(stdin, "begin\n")
	fmt.Fprintf(stdin, "put b1 v1\n")
	fmt.Fprintf(stdin, "put b2 v2\n")
	fmt.Fprintf(stdin, "del foo\n")
	fmt.Fprintf(stdin, "commit\n")

	line, err = reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "ACK 3") {
		t.Fatalf("expected ACK 3 for commit, got: %v, %s", err, line)
	}

	// verify foo is gone
	fmt.Fprintf(stdin, "get foo\n")
	line, err = reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "NOTFOUND 4" {
		t.Fatalf("expected NOTFOUND 4, got: %v, %s", err, line)
	}

	// verify b1 is present
	fmt.Fprintf(stdin, "get b1\n")
	line, err = reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "VAL 5 v1" {
		t.Fatalf("expected VAL 5 v1, got: %v, %s", err, line)
	}

	// 4. abort
	fmt.Fprintf(stdin, "begin\n")
	fmt.Fprintf(stdin, "put temp val\n")
	fmt.Fprintf(stdin, "abort\n")
	fmt.Fprintf(stdin, "get temp\n")
	line, err = reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "NOTFOUND 6" {
		t.Fatalf("expected NOTFOUND 6 after abort, got: %v, %s", err, line)
	}

	// 5. compact (Note: db.Compact() returns error until E4)
	fmt.Fprintf(stdin, "compact\n")
	line, err = reader.ReadString('\n')
	if err != nil || (!strings.HasPrefix(line, "ACK 7") && !strings.Contains(line, "ERR 7")) {
		t.Fatalf("expected ACK 7 or ERR 7 for compact, got: %v, %s", err, line)
	}
}

func TestServeB64ModeAnd1MiB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "serve-b64.db")

	cmd := exec.Command(kvdbBin, "serve", dbPath, "--b64")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdoutPipe)

	// Handshake
	readyLine, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(readyLine, "READY ") {
		t.Fatalf("expected READY, got: %v, %s", err, readyLine)
	}

	// 1. Basic PUT and GET
	k1 := base64.StdEncoding.EncodeToString([]byte("user:1"))
	v1 := base64.StdEncoding.EncodeToString([]byte("Alice"))
	fmt.Fprintf(stdin, "101 0 PUT %s %s\n", k1, v1)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ACK 101" {
		t.Fatalf("expected ACK 101, got: %v, %s", err, line)
	}

	fmt.Fprintf(stdin, "102 0 GET %s\n", k1)
	line, err = reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != fmt.Sprintf("VAL 102 %s", v1) {
		t.Fatalf("expected VAL 102 %s, got: %v, %s", v1, err, line)
	}

	// 2. MGET with found, not found, and empty values
	kEmpty := base64.StdEncoding.EncodeToString([]byte("empty-key"))
	fmt.Fprintf(stdin, "103 0 PUT %s -\n", kEmpty)
	line, _ = reader.ReadString('\n')
	if strings.TrimSpace(line) != "ACK 103" {
		t.Fatalf("expected ACK 103, got: %s", line)
	}

	kMissing := base64.StdEncoding.EncodeToString([]byte("missing"))
	fmt.Fprintf(stdin, "104 0 MGET %s %s %s\n", k1, kEmpty, kMissing)
	line, _ = reader.ReadString('\n')
	expectedMVAL := fmt.Sprintf("MVAL 104 %s - !", v1)
	if strings.TrimSpace(line) != expectedMVAL {
		t.Fatalf("expected %s, got: %s", expectedMVAL, line)
	}

	// 3. 1 MiB value through serve
	oneMiB := make([]byte, 1024*1024)
	for i := range oneMiB {
		oneMiB[i] = byte(i % 251)
	}
	kBig := base64.StdEncoding.EncodeToString([]byte("big-1mb"))
	vBig := base64.StdEncoding.EncodeToString(oneMiB)

	fmt.Fprintf(stdin, "105 1 PUT %s %s\n", kBig, vBig)
	line, err = reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ACK 105" {
		t.Fatalf("expected ACK 105, got: %v, %s", err, line)
	}

	fmt.Fprintf(stdin, "106 1 GET %s\n", kBig)
	line, err = reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "VAL 106 ") {
		t.Fatalf("expected VAL 106 ..., got: %v, prefix wrong", err)
	}
	gotValB64 := strings.TrimSpace(strings.TrimPrefix(line, "VAL 106 "))
	gotBytes, err := base64.StdEncoding.DecodeString(gotValB64)
	if err != nil || !bytes.Equal(gotBytes, oneMiB) {
		t.Fatalf("1 MiB round-trip mismatch: err=%v, len=%d", err, len(gotBytes))
	}

	// 4. BATCH (buffered <= 64 MiB)
	// 2 ops: PUT b1 v1, DEL b2
	kb1 := base64.StdEncoding.EncodeToString([]byte("b1"))
	vb1 := base64.StdEncoding.EncodeToString([]byte("vb1"))
	kb2 := base64.StdEncoding.EncodeToString([]byte("user:1"))
	// payload = 4 + (7 + 2 + 3) + (7 + 6 + 0) = 4 + 12 + 13 = 29
	fmt.Fprintf(stdin, "107 0 BATCH 2 29\nPUT %s %s\nDEL %s\n", kb1, vb1, kb2)
	line, err = reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ACK 107" {
		t.Fatalf("expected ACK 107 for batch, got: %v, %s", err, line)
	}

	// 5. COMPACT (Note: db.Compact() returns error until E4)
	fmt.Fprintf(stdin, "108 0 COMPACT\n")
	line, err = reader.ReadString('\n')
	if err != nil || (!strings.HasPrefix(line, "ACK 108") && !strings.Contains(line, "ERR 108")) {
		t.Fatalf("expected ACK 108 or ERR 108 for compact, got: %v, %s", err, line)
	}
}

func TestServeStreamedBatchOver64MiB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "serve-stream.db")

	cmd := exec.Command(kvdbBin, "serve", dbPath, "--b64")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdoutPipe)

	// Wait for READY
	readyLine, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(readyLine, "READY ") {
		t.Fatalf("expected READY, got: %v, %s", err, readyLine)
	}

	// 65 ops of 1 MiB each -> ~65 MiB > 64 MiB
	count := 65
	val1MB := make([]byte, 1024*1024)
	val1MBB64 := base64.StdEncoding.EncodeToString(val1MB)

	var payloadBytes int64 = 4
	for i := 0; i < count; i++ {
		k := fmt.Sprintf("stream-key-%04d", i)
		payloadBytes += 7 + int64(len(k)) + int64(len(val1MB))
	}

	// Send BATCH header
	fmt.Fprintf(stdin, "200 0 BATCH %d %d\n", count, payloadBytes)

	// Stream ops
	writer := bufio.NewWriter(stdin)
	for i := 0; i < count; i++ {
		k := fmt.Sprintf("stream-key-%04d", i)
		kB64 := base64.StdEncoding.EncodeToString([]byte(k))
		fmt.Fprintf(writer, "PUT %s %s\n", kB64, val1MBB64)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("failed to flush streamed batch: %v", err)
	}

	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ACK 200" {
		t.Fatalf("expected ACK 200 for streamed batch, got: %v, %s", err, line)
	}
}
