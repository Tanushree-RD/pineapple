package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/chethan-9817/pineapple/src/engine"
)

const maxBatchBufferingBytes = 64 * 1024 * 1024 // 64 MiB

func main() {
	if len(os.Args) < 2 {
		printUsageAndExit()
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "open":
		runOpen(args)
	case "put":
		runPut(args)
	case "get":
		runGet(args)
	case "del":
		runDel(args)
	case "batch":
		runBatch(args)
	case "dump":
		runDump(args)
	case "check":
		runCheck(args)
	case "serve":
		runServe(args)
	default:
		fmt.Fprintf(os.Stderr, "ERR unknown command: %s\n", cmd)
		os.Exit(1)
	}
}

func printUsageAndExit() {
	fmt.Fprintf(os.Stderr, "Usage: kvdb <command> [args...]\n")
	os.Exit(1)
}

func parseMaxBytesFlag(args []string) (path string, maxBytes int64, setMaxBytes bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--max-bytes" {
			if i+1 >= len(args) {
				return "", 0, false, errors.New("missing value for --max-bytes")
			}
			n, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil {
				return "", 0, false, fmt.Errorf("invalid --max-bytes value: %w", err)
			}
			maxBytes = n
			setMaxBytes = true
			i++
		} else if strings.HasPrefix(arg, "--max-bytes=") {
			n, err := strconv.ParseInt(strings.TrimPrefix(arg, "--max-bytes="), 10, 64)
			if err != nil {
				return "", 0, false, fmt.Errorf("invalid --max-bytes value: %w", err)
			}
			maxBytes = n
			setMaxBytes = true
		} else if !strings.HasPrefix(arg, "-") && path == "" {
			path = arg
		} else {
			return "", 0, false, fmt.Errorf("unexpected argument: %s", arg)
		}
	}
	if path == "" {
		return "", 0, false, errors.New("missing path argument")
	}
	return path, maxBytes, setMaxBytes, nil
}

func runOpen(args []string) {
	path, maxBytes, setMaxBytes, err := parseMaxBytesFlag(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}

	opts := engine.Options{
		MaxBytes:    maxBytes,
		SetMaxBytes: setMaxBytes,
	}

	db, err := engine.Open(path, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	stats := db.Stats()
	recoveryMs := float64(stats.Recovery.Microseconds()) / 1000.0
	fmt.Printf("keys=%d recovery_ms=%.3f tail=%s max_bytes=%d\n", stats.Keys, recoveryMs, stats.Tail, stats.MaxBytes)
	os.Exit(0)
}

func runPut(args []string) {
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "ERR usage: kvdb put <path> <key> <value>\n")
		os.Exit(1)
	}

	path := args[0]
	key, err := DecodeInput(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR invalid key hex: %v\n", err)
		os.Exit(1)
	}
	val, err := DecodeInput(args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR invalid value hex: %v\n", err)
		os.Exit(1)
	}

	db, err := engine.Open(path, engine.Options{SetMaxBytes: false})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	err = db.Put(key, val)
	if err != nil {
		if errors.Is(err, engine.ErrFull) {
			fmt.Println("ERR FULL")
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}

	fmt.Println("OK")
	os.Exit(0)
}

func runGet(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "ERR usage: kvdb get <path> <key>\n")
		os.Exit(1)
	}

	path := args[0]
	key, err := DecodeInput(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR invalid key hex: %v\n", err)
		os.Exit(1)
	}

	db, err := engine.Open(path, engine.Options{SetMaxBytes: false})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	val, found, err := db.Get(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}

	if !found {
		fmt.Println("NOTFOUND")
	} else {
		fmt.Println(EncodeOutput(val))
	}
	os.Exit(0)
}

func runDel(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "ERR usage: kvdb del <path> <key>\n")
		os.Exit(1)
	}

	path := args[0]
	key, err := DecodeInput(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR invalid key hex: %v\n", err)
		os.Exit(1)
	}

	db, err := engine.Open(path, engine.Options{SetMaxBytes: false})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	err = db.Delete(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}

	fmt.Println("OK")
	os.Exit(0)
}

func runDump(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "ERR usage: kvdb dump <path>\n")
		os.Exit(1)
	}

	path := args[0]
	db, err := engine.Open(path, engine.Options{SetMaxBytes: false})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	err = db.ForEach(func(key, value []byte) error {
		fmt.Printf("%s\t%s\n", EncodeOutput(key), EncodeOutput(value))
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runCheck(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "ERR usage: kvdb check <path>\n")
		os.Exit(1)
	}

	path := args[0]
	report, err := engine.Check(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}

	if !report.OK {
		fmt.Printf("CORRUPT %s\n", report.Reason)
		os.Exit(1)
	}

	fmt.Println("OK")
	for _, info := range report.Info {
		fmt.Printf("INFO %s\n", info)
	}
	os.Exit(0)
}

func parseBatchLine(line string) (*engine.Op, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return nil, nil
	}

	parts := strings.SplitN(trimmed, " ", 3)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "put":
		if len(parts) < 2 {
			return nil, errors.New("malformed put line")
		}
		key, err := DecodeInput(parts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid key: %w", err)
		}
		var val []byte
		if len(parts) >= 3 {
			val, err = DecodeInput(parts[2])
			if err != nil {
				return nil, fmt.Errorf("invalid value: %w", err)
			}
		}
		return &engine.Op{Kind: engine.OpPut, Key: key, Value: val}, nil

	case "del":
		if len(parts) < 2 {
			return nil, errors.New("malformed del line")
		}
		key, err := DecodeInput(parts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid key: %w", err)
		}
		return &engine.Op{Kind: engine.OpDelete, Key: key}, nil

	default:
		return nil, fmt.Errorf("unknown batch operation: %s", cmd)
	}
}

func runBatch(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "ERR usage: kvdb batch <path> <file>\n")
		os.Exit(1)
	}

	path := args[0]
	batchFile := args[1]

	f, err := os.Open(batchFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}

	// Pass 1: validate, count ops, and calculate payload_bytes
	scanner := bufio.NewReader(f)
	var opCount int
	var payloadBytes int64 = 4 // op_count u32

	for {
		line, readErr := scanner.ReadString('\n')
		if len(line) > 0 {
			op, err := parseBatchLine(line)
			if err != nil {
				f.Close()
				fmt.Fprintf(os.Stderr, "ERR %v\n", err)
				os.Exit(1)
			}
			if op != nil {
				opCount++
				payloadBytes += 7 + int64(len(op.Key)) + int64(len(op.Value))
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			f.Close()
			fmt.Fprintf(os.Stderr, "ERR reading batch file: %v\n", readErr)
			os.Exit(1)
		}
	}
	f.Close()

	if opCount == 0 {
		fmt.Fprintf(os.Stderr, "ERR empty batch\n")
		os.Exit(1)
	}

	db, err := engine.Open(path, engine.Options{SetMaxBytes: false})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Pass 2: execute batch
	if payloadBytes <= maxBatchBufferingBytes {
		// Buffered path
		f2, err := os.Open(batchFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERR %v\n", err)
			os.Exit(1)
		}
		defer f2.Close()

		r2 := bufio.NewReader(f2)
		ops := make([]engine.Op, 0, opCount)
		for {
			line, readErr := r2.ReadString('\n')
			if len(line) > 0 {
				op, _ := parseBatchLine(line)
				if op != nil {
					ops = append(ops, *op)
				}
			}
			if readErr != nil {
				break
			}
		}

		err = db.Batch(ops)
	} else {
		// Streamed path via ApplyStream
		f2, err := os.Open(batchFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERR %v\n", err)
			os.Exit(1)
		}
		defer f2.Close()

		r2 := bufio.NewReader(f2)
		next := func() (engine.Op, error) {
			for {
				line, readErr := r2.ReadString('\n')
				if len(line) > 0 {
					op, parseErr := parseBatchLine(line)
					if parseErr != nil {
						return engine.Op{}, parseErr
					}
					if op != nil {
						return *op, nil
					}
				}
				if readErr != nil {
					return engine.Op{}, readErr
				}
			}
		}

		err = db.ApplyStream(opCount, payloadBytes, next, 0)
	}

	if err != nil {
		if errors.Is(err, engine.ErrFull) {
			fmt.Println("ERR FULL")
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}

	fmt.Println("OK")
	os.Exit(0)
}
