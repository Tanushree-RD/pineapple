//go:build linux

package main

import "syscall"

// onTmpfs reports whether path lives on tmpfs (RAM). There fsync costs nothing, so the database
// acknowledges tens of thousands of ops per iteration and the file grows hundreds of times faster
// than on a real disk; runs get slow and the durability story is less honest.
func onTmpfs(path string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false
	}
	const tmpfsMagic = 0x01021994
	return int64(st.Type) == tmpfsMagic
}
