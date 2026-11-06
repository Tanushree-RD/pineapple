//go:build unix

package engine

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func benchPlainFsync(t *testing.T, f *os.File) {
	buf := make([]byte, 100)
	start := time.Now()
	for i := 0; i < 1000; i++ {
		f.WriteAt(buf, int64(i*100))
		if err := syscall.Fsync(int(f.Fd())); err != nil {
			t.Logf("FSYNC plain fsync(2): FAILED: %v", err)
			return
		}
	}
	d := time.Since(start)
	t.Logf("FSYNC plain fsync(2): 1000 writes in %v = %.0f per second", d, 1000/d.Seconds())
}
