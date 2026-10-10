//go:build !unix

package main

import "errors"

// mkfifo: vmaf reads its streams through named pipes, which this system
// does not have.
func mkfifo(string) error { return errors.New("-vmaf needs named pipes (Linux, macOS)") }
