package main

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chethan-9817/pineapple/src/engine"
)

type laneManager struct {
	mu    sync.Mutex
	lanes map[int]chan func()
}

func newLaneManager() *laneManager {
	return &laneManager{
		lanes: make(map[int]chan func()),
	}
}

func (lm *laneManager) dispatch(lane int, job func()) {
	lm.mu.Lock()
	ch, ok := lm.lanes[lane]
	if !ok {
		ch = make(chan func(), 128)
		lm.lanes[lane] = ch
		go func() {
			for f := range ch {
				f()
			}
		}()
	}
	lm.mu.Unlock()
	ch <- job
}

func parseServeArgs(args []string) (path string, maxBytes int64, setMaxBytes bool, b64Mode bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--b64" {
			b64Mode = true
		} else if arg == "--max-bytes" {
			if i+1 >= len(args) {
				return "", 0, false, false, errors.New("missing value for --max-bytes")
			}
			n, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil {
				return "", 0, false, false, fmt.Errorf("invalid --max-bytes value: %w", err)
			}
			maxBytes = n
			setMaxBytes = true
			i++
		} else if strings.HasPrefix(arg, "--max-bytes=") {
			n, err := strconv.ParseInt(strings.TrimPrefix(arg, "--max-bytes="), 10, 64)
			if err != nil {
				return "", 0, false, false, fmt.Errorf("invalid --max-bytes value: %w", err)
			}
			maxBytes = n
			setMaxBytes = true
		} else if !strings.HasPrefix(arg, "-") && path == "" {
			path = arg
		} else {
			return "", 0, false, false, fmt.Errorf("unexpected argument: %s", arg)
		}
	}
	if path == "" {
		return "", 0, false, false, errors.New("missing path argument")
	}
	return path, maxBytes, setMaxBytes, b64Mode, nil
}

func runServe(args []string) {
	path, maxBytes, setMaxBytes, b64Mode, err := parseServeArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}

	var stdoutMu sync.Mutex
	outWriter := bufio.NewWriter(os.Stdout)

	writeReply := func(format string, a ...interface{}) {
		stdoutMu.Lock()
		defer stdoutMu.Unlock()
		fmt.Fprintf(outWriter, format, a...)
		outWriter.Flush()
	}

	opts := engine.Options{
		MaxBytes:    maxBytes,
		SetMaxBytes: setMaxBytes,
		OnCommit: func(tag uint64) {
			stdoutMu.Lock()
			defer stdoutMu.Unlock()
			fmt.Fprintf(outWriter, "ACK %d\n", tag)
			outWriter.Flush()
			engine.Hit("CP-W07")
		},
	}

	db, err := engine.Open(path, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	stats := db.Stats()
	recoveryMs := float64(stats.Recovery.Microseconds()) / 1000.0

	// Handshake: first line always
	writeReply("READY keys=%d recovery_ms=%.3f max_bytes=%d\n", stats.Keys, recoveryMs, stats.MaxBytes)

	reader := bufio.NewReader(os.Stdin)

	if b64Mode {
		serveB64(db, reader, &stdoutMu, outWriter)
	} else {
		serveText(db, reader, &stdoutMu, outWriter)
	}
}

func decodeB64Val(s string) ([]byte, error) {
	if s == "-" {
		return []byte{}, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

func encodeB64Val(b []byte) string {
	if len(b) == 0 {
		return "-"
	}
	return base64.StdEncoding.EncodeToString(b)
}

func handleServeError(err error, id uint64, stdoutMu *sync.Mutex, outWriter *bufio.Writer) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()

	if errors.Is(err, engine.ErrFull) {
		fmt.Fprintf(outWriter, "ERR %d FULL\n", id)
		outWriter.Flush()
	} else if errors.Is(err, engine.ErrIO) {
		fmt.Fprintf(outWriter, "ERR %d IO\n", id)
		outWriter.Flush()
		stdoutMu.Unlock()
		os.Exit(3)
	} else if errors.Is(err, engine.ErrInvalid) {
		fmt.Fprintf(outWriter, "ERR %d INVALID\n", id)
		outWriter.Flush()
	} else {
		fmt.Fprintf(outWriter, "ERR %d %v\n", id, err)
		outWriter.Flush()
	}
}

func serveB64(db *engine.DB, reader *bufio.Reader, stdoutMu *sync.Mutex, outWriter *bufio.Writer) {
	lm := newLaneManager()

	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			if len(line) == 0 {
				if readErr != nil {
					break
				}
				continue
			}

			parts := strings.Split(line, " ")
			if len(parts) < 3 {
				continue
			}

			id, err := strconv.ParseUint(parts[0], 10, 64)
			if err != nil {
				continue
			}
			lane, err := strconv.Atoi(parts[1])
			if err != nil {
				continue
			}
			cmd := strings.ToUpper(parts[2])

			switch cmd {
			case "PUT":
				if len(parts) < 5 {
					handleServeError(engine.ErrInvalid, id, stdoutMu, outWriter)
					continue
				}
				key, err1 := decodeB64Val(parts[3])
				val, err2 := decodeB64Val(parts[4])
				if err1 != nil || err2 != nil {
					handleServeError(engine.ErrInvalid, id, stdoutMu, outWriter)
					continue
				}

				lm.dispatch(lane, func() {
					err := db.Apply([]engine.Op{{Kind: engine.OpPut, Key: key, Value: val}}, id)
					if err != nil {
						handleServeError(err, id, stdoutMu, outWriter)
					}
				})

			case "DEL":
				if len(parts) < 4 {
					handleServeError(engine.ErrInvalid, id, stdoutMu, outWriter)
					continue
				}
				key, err := decodeB64Val(parts[3])
				if err != nil {
					handleServeError(engine.ErrInvalid, id, stdoutMu, outWriter)
					continue
				}

				lm.dispatch(lane, func() {
					err := db.Apply([]engine.Op{{Kind: engine.OpDelete, Key: key}}, id)
					if err != nil {
						handleServeError(err, id, stdoutMu, outWriter)
					}
				})

			case "GET":
				if len(parts) < 4 {
					handleServeError(engine.ErrInvalid, id, stdoutMu, outWriter)
					continue
				}
				key, err := decodeB64Val(parts[3])
				if err != nil {
					handleServeError(engine.ErrInvalid, id, stdoutMu, outWriter)
					continue
				}

				lm.dispatch(lane, func() {
					val, found, err := db.Get(key)
					if err != nil {
						handleServeError(err, id, stdoutMu, outWriter)
						return
					}
					stdoutMu.Lock()
					defer stdoutMu.Unlock()
					if !found {
						fmt.Fprintf(outWriter, "NOTFOUND %d\n", id)
					} else {
						fmt.Fprintf(outWriter, "VAL %d %s\n", id, encodeB64Val(val))
					}
					outWriter.Flush()
				})

			case "MGET":
				if len(parts) < 4 {
					handleServeError(engine.ErrInvalid, id, stdoutMu, outWriter)
					continue
				}
				keys := make([][]byte, 0, len(parts)-3)
				var parseErr bool
				for _, kp := range parts[3:] {
					k, err := decodeB64Val(kp)
					if err != nil {
						parseErr = true
						break
					}
					keys = append(keys, k)
				}
				if parseErr {
					handleServeError(engine.ErrInvalid, id, stdoutMu, outWriter)
					continue
				}

				lm.dispatch(lane, func() {
					vals, found, err := db.MultiGet(keys)
					if err != nil {
						handleServeError(err, id, stdoutMu, outWriter)
						return
					}
					results := make([]string, len(keys))
					for i := range keys {
						if !found[i] {
							results[i] = "!"
						} else {
							results[i] = encodeB64Val(vals[i])
						}
					}
					stdoutMu.Lock()
					defer stdoutMu.Unlock()
					fmt.Fprintf(outWriter, "MVAL %d %s\n", id, strings.Join(results, " "))
					outWriter.Flush()
				})

			case "COMPACT":
				lm.dispatch(lane, func() {
					err := db.Compact()
					if err != nil {
						handleServeError(err, id, stdoutMu, outWriter)
						return
					}
					stdoutMu.Lock()
					defer stdoutMu.Unlock()
					fmt.Fprintf(outWriter, "ACK %d\n", id)
					outWriter.Flush()
					engine.Hit("CP-W07")
				})

			case "BATCH":
				if len(parts) < 5 {
					handleServeError(engine.ErrInvalid, id, stdoutMu, outWriter)
					continue
				}
				n, err1 := strconv.Atoi(parts[3])
				payloadBytes, err2 := strconv.ParseInt(parts[4], 10, 64)
				if err1 != nil || err2 != nil || n < 1 {
					handleServeError(engine.ErrInvalid, id, stdoutMu, outWriter)
					continue
				}

				if payloadBytes <= maxBatchBufferingBytes {
					// Read all n lines directly
					ops := make([]engine.Op, 0, n)
					var actualPayload int64 = 4
					var readBatchErr error

					for i := 0; i < n; i++ {
						opLine, rErr := reader.ReadString('\n')
						if rErr != nil && len(opLine) == 0 {
							readBatchErr = rErr
							break
						}
						opLine = strings.TrimRight(opLine, "\r\n")
						opParts := strings.Split(opLine, " ")
						if len(opParts) < 2 {
							readBatchErr = engine.ErrInvalid
							continue
						}
						opCmd := strings.ToUpper(opParts[0])
						switch opCmd {
						case "PUT":
							if len(opParts) < 3 {
								readBatchErr = engine.ErrInvalid
								continue
							}
							k, e1 := decodeB64Val(opParts[1])
							v, e2 := decodeB64Val(opParts[2])
							if e1 != nil || e2 != nil {
								readBatchErr = engine.ErrInvalid
								continue
							}
							ops = append(ops, engine.Op{Kind: engine.OpPut, Key: k, Value: v})
							actualPayload += 7 + int64(len(k)) + int64(len(v))
						case "DEL":
							k, e1 := decodeB64Val(opParts[1])
							if e1 != nil {
								readBatchErr = engine.ErrInvalid
								continue
							}
							ops = append(ops, engine.Op{Kind: engine.OpDelete, Key: k})
							actualPayload += 7 + int64(len(k))
						default:
							readBatchErr = engine.ErrInvalid
						}
					}

					lm.dispatch(lane, func() {
						if readBatchErr != nil || actualPayload != payloadBytes || len(ops) != n {
							handleServeError(engine.ErrInvalid, id, stdoutMu, outWriter)
							return
						}
						err := db.Apply(ops, id)
						if err != nil {
							handleServeError(err, id, stdoutMu, outWriter)
						}
					})

				} else {
					// Streamed BATCH (> 64 MiB)
					// Channel to stream op lines from reader to lane
					lineCh := make(chan string, 16)
					doneCh := make(chan struct{})

					lm.dispatch(lane, func() {
						defer close(doneCh)
						next := func() (engine.Op, error) {
							opLine, ok := <-lineCh
							if !ok {
								return engine.Op{}, io.EOF
							}
							opLine = strings.TrimRight(opLine, "\r\n")
							opParts := strings.Split(opLine, " ")
							if len(opParts) < 2 {
								return engine.Op{}, engine.ErrInvalid
							}
							opCmd := strings.ToUpper(opParts[0])
							switch opCmd {
							case "PUT":
								if len(opParts) < 3 {
									return engine.Op{}, engine.ErrInvalid
								}
								k, e1 := decodeB64Val(opParts[1])
								v, e2 := decodeB64Val(opParts[2])
								if e1 != nil || e2 != nil {
									return engine.Op{}, engine.ErrInvalid
								}
								return engine.Op{Kind: engine.OpPut, Key: k, Value: v}, nil
							case "DEL":
								k, e1 := decodeB64Val(opParts[1])
								if e1 != nil {
									return engine.Op{}, engine.ErrInvalid
								}
								return engine.Op{Kind: engine.OpDelete, Key: k}, nil
							default:
								return engine.Op{}, engine.ErrInvalid
							}
						}

						err := db.ApplyStream(n, payloadBytes, next, id)
						if err != nil {
							handleServeError(err, id, stdoutMu, outWriter)
						}
					})

					// Feed the n lines, always consuming exactly n lines
					for i := 0; i < n; i++ {
						opLine, rErr := reader.ReadString('\n')
						select {
						case lineCh <- opLine:
						case <-doneCh:
							// ApplyStream ended early, discard line
						}
						if rErr != nil {
							break
						}
					}
					close(lineCh)
					<-doneCh
				}
			}
		}

		if readErr != nil {
			break
		}
	}
}

func serveText(db *engine.DB, reader *bufio.Reader, stdoutMu *sync.Mutex, outWriter *bufio.Writer) {
	var opId uint64
	var inBatch bool
	var batchOps []engine.Op

	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(line)
			if len(trimmed) > 0 {
				parts := strings.SplitN(trimmed, " ", 3)
				cmd := strings.ToLower(parts[0])

				if inBatch && cmd != "commit" && cmd != "abort" {
					op, err := parseBatchLine(trimmed)
					if err != nil || op == nil {
						stdoutMu.Lock()
						fmt.Fprintf(outWriter, "ERR %d INVALID\n", atomic.AddUint64(&opId, 1))
						outWriter.Flush()
						stdoutMu.Unlock()
					} else {
						batchOps = append(batchOps, *op)
					}
					if readErr != nil {
						break
					}
					continue
				}

				switch cmd {
				case "begin":
					inBatch = true
					batchOps = nil

				case "abort":
					inBatch = false
					batchOps = nil

				case "commit":
					inBatch = false
					id := atomic.AddUint64(&opId, 1)
					if len(batchOps) == 0 {
						stdoutMu.Lock()
						fmt.Fprintf(outWriter, "ERR %d INVALID\n", id)
						outWriter.Flush()
						stdoutMu.Unlock()
						continue
					}
					err := db.Apply(batchOps, id)
					if err != nil {
						handleServeError(err, id, stdoutMu, outWriter)
					}
					batchOps = nil

				case "put":
					id := atomic.AddUint64(&opId, 1)
					if len(parts) < 2 {
						stdoutMu.Lock()
						fmt.Fprintf(outWriter, "ERR %d INVALID\n", id)
						outWriter.Flush()
						stdoutMu.Unlock()
						continue
					}
					key, err1 := DecodeInput(parts[1])
					var val []byte
					var err2 error
					if len(parts) >= 3 {
						val, err2 = DecodeInput(parts[2])
					}
					if err1 != nil || err2 != nil {
						stdoutMu.Lock()
						fmt.Fprintf(outWriter, "ERR %d INVALID\n", id)
						outWriter.Flush()
						stdoutMu.Unlock()
						continue
					}
					err := db.Apply([]engine.Op{{Kind: engine.OpPut, Key: key, Value: val}}, id)
					if err != nil {
						handleServeError(err, id, stdoutMu, outWriter)
					}

				case "del":
					id := atomic.AddUint64(&opId, 1)
					if len(parts) < 2 {
						stdoutMu.Lock()
						fmt.Fprintf(outWriter, "ERR %d INVALID\n", id)
						outWriter.Flush()
						stdoutMu.Unlock()
						continue
					}
					key, err := DecodeInput(parts[1])
					if err != nil {
						stdoutMu.Lock()
						fmt.Fprintf(outWriter, "ERR %d INVALID\n", id)
						outWriter.Flush()
						stdoutMu.Unlock()
						continue
					}
					err = db.Apply([]engine.Op{{Kind: engine.OpDelete, Key: key}}, id)
					if err != nil {
						handleServeError(err, id, stdoutMu, outWriter)
					}

				case "get":
					id := atomic.AddUint64(&opId, 1)
					if len(parts) < 2 {
						stdoutMu.Lock()
						fmt.Fprintf(outWriter, "ERR %d INVALID\n", id)
						outWriter.Flush()
						stdoutMu.Unlock()
						continue
					}
					key, err := DecodeInput(parts[1])
					if err != nil {
						stdoutMu.Lock()
						fmt.Fprintf(outWriter, "ERR %d INVALID\n", id)
						outWriter.Flush()
						stdoutMu.Unlock()
						continue
					}
					val, found, err := db.Get(key)
					stdoutMu.Lock()
					if err != nil {
						fmt.Fprintf(outWriter, "ERR %d %v\n", id, err)
					} else if !found {
						fmt.Fprintf(outWriter, "NOTFOUND %d\n", id)
					} else {
						fmt.Fprintf(outWriter, "VAL %d %s\n", id, EncodeOutput(val))
					}
					outWriter.Flush()
					stdoutMu.Unlock()

				case "compact":
					id := atomic.AddUint64(&opId, 1)
					err := db.Compact()
					if err != nil {
						handleServeError(err, id, stdoutMu, outWriter)
					} else {
						stdoutMu.Lock()
						fmt.Fprintf(outWriter, "ACK %d\n", id)
						outWriter.Flush()
						engine.Hit("CP-W07")
						stdoutMu.Unlock()
					}
				}
			}
		}

		if readErr != nil {
			break
		}
	}
}
