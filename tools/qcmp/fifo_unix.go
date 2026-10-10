//go:build unix

package main

import "syscall"

// mkfifo makes the named pipe vmaf reads a stream from.
func mkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }
