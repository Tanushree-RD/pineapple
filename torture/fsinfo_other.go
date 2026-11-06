//go:build !linux

package main

func onTmpfs(path string) bool { return false }
