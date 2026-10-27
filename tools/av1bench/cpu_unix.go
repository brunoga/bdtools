//go:build unix

package main

import "syscall"

// cpuSeconds is the process's CPU time so far (user and system).
func cpuSeconds() float64 {
	var u syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &u) != nil {
		return 0
	}
	tv := func(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }
	return tv(u.Utime) + tv(u.Stime)
}
