//go:build !unix

package main

// cpuSeconds is not measured here.
func cpuSeconds() float64 { return 0 }
