
## 2026-11-06 16:34 IST · E1 · format + streaming frame writer
- **Files:** src/engine/format.go, superblock.go, frame.go, payload.go, format_test.go, payload_test.go
- **What:** Byte-exact SPEC 4 encode/decode. Superblock (68 bytes in a 4 KiB slot, CRC32C, generation Next with overflow check, ChooseSuperblock, InitialImage). Frame header (EncodeHeader/DecodeHeader = checks 1-2), ScanFrame = SPEC 7.2 checks 1-7 over an io.ReaderAt (streams the CRC64 in 1 MiB steps, never allocates from L before check 5, handles the SHORT_SENTINEL case). FrameWriter streams header, payload, CRC64 and end_magic with an incremental CRC64 and writes of at most 4 MiB. BatchWriter/WriteBatchFrame and WriteKeydirFrame encode payloads; ParseBatch/ParseKeydir are the exact parsers (check 8). Typed FrameError names the failed check for recovery/Check reporting.
- **Crash points touched:** none yet (pure format code; CP markers arrive in E2)
- **Tests:** make check PASS. 22 tests: golden bytes for superblock and header, every used superblock byte flip rejected, torn-newest fallback, header/frame/sentinel malformed cases, payload_len = MAX_PAYLOAD+1 and 2^64-1 refused without allocating, short sentinel 0..8 bytes missing, 12 MiB batch never written in a single write over 4 MiB, 10,000-op batch parses, all malformed BATCH and KEYDIR cases from MASTER_PROMPT E1, empty KEYDIR frame is 72 bytes (SPEC 10).
- **Status:** E1 done. Note: ScanFrame only validates; applying ops is the caller's job after BOTH ScanFrame and ParseBatch succeed (two steps keep P-atomicity: nothing applied from a frame that fails check 8).
- **NEEDS:** none
- **Next:** E2 durable commit path (locking, create via tmp+rename, committer, recovery, poison). Unix-only pieces (flock, directory fsync) go in build-tagged files.
writer with tests
