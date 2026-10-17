package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Batches (an --input that is a directory of sources): each source the
// directory holds, at any depth (disc images, BDMV folders, Matroska, MP4
// and QuickTime files, transport streams), converted by a run of its own
// with the same settings into --output, a directory, under the same
// relative path. What is done is noted in the output directory, so a
// batch run again (or watching, --watch) converts only what is new.

// batchState is the output directory's note of the sources converted.
const batchState = ".bdtools-batch.json"

// sourceExts are the files a batch converts.
var sourceExts = map[string]bool{".iso": true, ".mkv": true, ".mp4": true, ".m4v": true, ".mov": true, ".m2ts": true, ".ts": true}

// isDisc reports whether a directory is a disc's (a BDMV folder, or one
// holding it).
func isDisc(dir string) bool {
	if strings.EqualFold(filepath.Base(dir), "BDMV") {
		return true
	}
	st, err := os.Stat(filepath.Join(dir, "BDMV"))
	return err == nil && st.IsDir()
}

// isBatch reports whether an input is a directory of sources.
func isBatch(in string) bool {
	st, err := os.Stat(in)
	return err == nil && st.IsDir() && !isDisc(in)
}

// batchSources lists a directory's sources, by their paths relative to it.
func batchSources(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable corner is skipped
		}
		if d.IsDir() {
			if path != root && isDisc(path) {
				out = append(out, path)
				return filepath.SkipDir
			}
			if strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if sourceExts[strings.ToLower(filepath.Ext(path))] {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// settled reports whether a source is done arriving: nothing in it
// changed for a minute (a copy in progress is not converted yet).
func settled(path string) bool {
	var newest time.Time
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil {
			if info, err := d.Info(); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		return nil
	})
	return time.Since(newest) > time.Minute
}

// replaceArg gives argv with a flag's value replaced (in either of its
// spellings, - or --, with = or apart).
func replaceArg(argv []string, name, value string) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		trimmed := strings.TrimLeft(a, "-")
		switch {
		case (a == "-"+name || a == "--"+name) && i+1 < len(argv):
			out = append(out, a, value)
			i++
		case strings.HasPrefix(trimmed, name+"=") && strings.HasPrefix(a, "-"):
			out = append(out, "--"+name+"="+value)
		default:
			out = append(out, a)
		}
	}
	return out
}

// runBatch converts a directory's sources, and with watch goes on
// converting what arrives.
func runBatch(argv []string, in, outDir, ext string, watch bool, stdout, stderr *os.File) int {
	if outDir == "" {
		fmt.Fprintln(stderr, "bdtools: a batch (--input a directory) needs --output, a directory")
		return 2
	}
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		fmt.Fprintln(stderr, "bdtools:", err)
		return 1
	}
	statePath := filepath.Join(outDir, batchState)
	done := map[string]string{}
	if b, err := os.ReadFile(statePath); err == nil { //nolint:gosec // the operator's directory
		_ = json.Unmarshal(b, &done)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	failed := 0
	for {
		sources, err := batchSources(in)
		if err != nil {
			fmt.Fprintln(stderr, "bdtools:", err)
			return 1
		}
		for _, src := range sources {
			if _, ok := done[src]; ok {
				continue
			}
			if watch && !settled(src) {
				continue // still arriving
			}
			rel, _ := filepath.Rel(in, src)
			base := strings.TrimSuffix(rel, filepath.Ext(rel))
			if isDisc(src) && strings.EqualFold(filepath.Base(src), "BDMV") {
				base = filepath.Dir(rel)
			}
			out := filepath.Join(outDir, base+ext)
			if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
				fmt.Fprintln(stderr, "bdtools:", err)
				return 1
			}
			fmt.Fprintf(stderr, "bdtools: batch: %s -> %s\n", rel, out)
			args := replaceArg(replaceArg(argv, "input", src), "output", out)
			if code := run(args, stdout, stderr); code != 0 {
				fmt.Fprintf(stderr, "bdtools: batch: %s failed (exit %d); going on\n", rel, code)
				failed++
				if !watch {
					continue
				}
			}
			done[src] = out
			if b, err := json.MarshalIndent(done, "", "  "); err == nil {
				_ = os.WriteFile(statePath, b, 0o600)
			}
		}
		if !watch {
			break
		}
		select {
		case <-stop:
			return 0
		case <-time.After(time.Minute):
		}
	}
	if failed > 0 {
		fmt.Fprintf(stderr, "bdtools: batch: %d of the sources failed\n", failed)
		return 1
	}
	return 0
}
