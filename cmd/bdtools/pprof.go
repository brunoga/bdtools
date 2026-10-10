package main

import (
	"net/http"
	_ "net/http/pprof" //nolint:gosec // served only when asked for, on the address given
	"os"
	"time"
)

// BDTOOLS_PPROF=localhost:6060 serves Go's profiles (net/http/pprof) while
// bdtools runs: for finding where its memory or time goes.
func init() {
	addr := os.Getenv("BDTOOLS_PPROF")
	if addr == "" {
		return
	}
	go func() {
		srv := &http.Server{Addr: addr, ReadHeaderTimeout: 10 * time.Second}
		_ = srv.ListenAndServe()
	}()
}
