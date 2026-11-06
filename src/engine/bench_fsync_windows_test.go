//go:build windows

package engine

import (
	"os"
	"testing"
)

func benchPlainFsync(t *testing.T, f *os.File) { t.Log("FSYNC plain fsync(2) does not exist on Windows (dev machine only)") }
