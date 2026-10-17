package mvc

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// frameMD5 is the MD5 of a frame's planes as ffmpeg's framemd5 muxer
// hashes them (yuv420p, cropped).
func frameMD5(f *Frame) string {
	h := md5.New()
	for y := 0; y < f.Height; y++ {
		h.Write(f.Y[y*f.StrideY : y*f.StrideY+f.Width])
	}
	for _, pl := range [][]byte{f.Cb, f.Cr} {
		for y := 0; y < f.Height/2; y++ {
			h.Write(pl[y*f.StrideC : y*f.StrideC+f.Width/2])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func readFrameMD5s(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // test data
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		fs := strings.Split(line, ",")
		out = append(out, strings.TrimSpace(fs[len(fs)-1]))
	}
	return out, sc.Err()
}

// The interlaced conformance streams (field pictures, MBAFF and both),
// decoded picture for picture as ffmpeg decodes them: the ones in
// testdata, or the directory BDTOOLS_CONF_IL names (the full JVT set),
// with ref/<name>.md5 holding ffmpeg's framemd5 output for each.
func TestInterlacedConformance(t *testing.T) {
	dir := os.Getenv("BDTOOLS_CONF_IL")
	if dir == "" {
		dir = "../testdata/conformance/interlaced"
	}
	refs, _ := filepath.Glob(filepath.Join(dir, "ref", "*.md5"))
	sort.Strings(refs)
	for _, ref := range refs {
		name := strings.TrimSuffix(filepath.Base(ref), ".md5")
		if only := os.Getenv("BDTOOLS_CONF_IL_ONLY"); only != "" && !strings.Contains(name, only) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			want, err := readFrameMD5s(ref)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err != nil {
				path = filepath.Join(dir, "frext", name)
			}
			data, err := os.ReadFile(path) //nolint:gosec // test data
			if err != nil {
				t.Skip(err)
			}
			d := NewDecoder(Options{})
			var got []string
			drain := func() {
				for {
					f, ok := d.NextFrame()
					if !ok {
						return
					}
					got = append(got, frameMD5(f.Base))
					f.Release()
				}
			}
			var derr error
			for i := 0; i < len(data); i += 1 << 16 {
				if e := d.Decode(data[i:min(i+1<<16, len(data))]); e != nil && derr == nil {
					derr = e
				}
				drain()
			}
			if e := d.Flush(); e != nil && derr == nil {
				derr = e
			}
			drain()
			match := 0
			first := -1
			for i := range min(len(got), len(want)) {
				if got[i] == want[i] {
					match++
				} else if first < 0 {
					first = i
				}
			}
			if len(got) != len(want) || match != len(want) {
				t.Errorf("%d of %d frames match (%d decoded), first mismatch %d, error %v", match, len(want), len(got), first, derr)
			}
		})
	}
}

// TestDumpYUV writes BDTOOLS_DUMP_IN's frames to BDTOOLS_DUMP_OUT as
// yuv420p, a debugging aid.
func TestDumpYUV(t *testing.T) {
	in, out := os.Getenv("BDTOOLS_DUMP_IN"), os.Getenv("BDTOOLS_DUMP_OUT")
	if in == "" || out == "" {
		t.Skip("BDTOOLS_DUMP_IN/OUT not set")
	}
	data, err := os.ReadFile(in) //nolint:gosec // test data
	if err != nil {
		t.Fatal(err)
	}
	w, err := os.Create(out) //nolint:gosec // test output
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	threads := 0
	if os.Getenv("BDTOOLS_DUMP_SERIAL") != "" {
		threads = 1
	}
	d := NewDecoder(Options{Threads: threads})
	drain := func() {
		for {
			sf, ok := d.NextFrame()
			if !ok {
				return
			}
			f := sf.Base
			for y := 0; y < f.Height; y++ {
				w.Write(f.Y[y*f.StrideY : y*f.StrideY+f.Width])
			}
			for _, pl := range [][]byte{f.Cb, f.Cr} {
				for y := 0; y < f.Height/2; y++ {
					w.Write(pl[y*f.StrideC : y*f.StrideC+f.Width/2])
				}
			}
			sf.Release()
		}
	}
	_ = d.Decode(data)
	drain()
	_ = d.Flush()
	drain()
}
