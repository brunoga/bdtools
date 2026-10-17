package av1

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// The encoder's streams checked against a real decoder: written as IVF,
// decoded by ffmpeg's libdav1d (when there is one) to raw YUV, which must
// be the encoder's own reconstruction, sample for sample.

// ivf wraps temporal units in an IVF file.
func ivf(w, h, num, den int, units [][]byte) []byte {
	var b bytes.Buffer
	hdr := make([]byte, 32)
	copy(hdr, "DKIF")
	binary.LittleEndian.PutUint16(hdr[6:], 32)
	copy(hdr[8:], "AV01")
	binary.LittleEndian.PutUint16(hdr[12:], uint16(w))   //nolint:gosec // a test size
	binary.LittleEndian.PutUint16(hdr[14:], uint16(h))   //nolint:gosec // a test size
	binary.LittleEndian.PutUint32(hdr[16:], uint32(num)) //nolint:gosec // a rate
	binary.LittleEndian.PutUint32(hdr[20:], uint32(den)) //nolint:gosec // a rate
	binary.LittleEndian.PutUint32(hdr[24:], uint32(len(units)))
	b.Write(hdr)
	for i, u := range units {
		fh := make([]byte, 12)
		binary.LittleEndian.PutUint32(fh, uint32(len(u))) //nolint:gosec // a test unit
		binary.LittleEndian.PutUint64(fh[4:], uint64(i))  //nolint:gosec // an index
		b.Write(fh)
		b.Write(u)
	}
	return b.Bytes()
}

// needDav1d skips a test without ffmpeg's libdav1d (before it spends
// time encoding).
func needDav1d(t *testing.T) {
	t.Helper()
	dav1dOnce.Do(func() {
		ff, err := exec.LookPath("ffmpeg")
		if err != nil {
			return
		}
		out, err := exec.CommandContext(context.Background(), ff, "-hide_banner", "-decoders").Output() //nolint:gosec // a test
		dav1dOK = err == nil && bytes.Contains(out, []byte("libdav1d"))
	})
	if !dav1dOK {
		t.Skip("no ffmpeg with libdav1d")
	}
}

var (
	dav1dOnce sync.Once
	dav1dOK   bool
)

// dav1dDecode decodes an IVF file to its pictures' planes, 8 or 16 bits a
// sample; it skips the test without ffmpeg's libdav1d.
func dav1dDecode(t *testing.T, data []byte, w, h, depth int) [][]byte {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	if out, err := exec.CommandContext(t.Context(), ff, "-hide_banner", "-decoders").Output(); err != nil || !bytes.Contains(out, []byte("libdav1d")) {
		t.Skip("no libdav1d in ffmpeg")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.ivf")
	if err := os.WriteFile(in, data, 0o600); err != nil {
		t.Fatal(err)
	}
	pix := "yuv420p"
	bps := 1
	if depth > 8 {
		pix, bps = "yuv420p10le", 2
	}
	out := filepath.Join(dir, "out.yuv")
	cmd := exec.CommandContext(t.Context(), ff, "-hide_banner", "-loglevel", "error", "-c:v", "libdav1d", "-i", in, "-f", "rawvideo", "-pix_fmt", pix, out) //nolint:gosec // a test
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("libdav1d: %v: %s", err, msg)
	}
	b, err := os.ReadFile(out) //nolint:gosec // the test's
	if err != nil {
		t.Fatal(err)
	}
	cw, ch := (w+1)/2, (h+1)/2
	size := (w*h + 2*cw*ch) * bps
	var pics [][]byte
	for len(b) >= size {
		pics = append(pics, b[:size])
		b = b[size:]
	}
	return pics
}
