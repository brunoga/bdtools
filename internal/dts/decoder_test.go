package dts

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// decodeAll decodes every core frame of a raw DTS stream.
func decodeAll(t *testing.T, b []byte) []*Frame {
	t.Helper()
	var d Decoder
	var out []*Frame
	for len(b) > 0 {
		n := FrameSize(b)
		if n == 0 || n > len(b) {
			t.Fatalf("no frame at %d bytes left", len(b))
		}
		f, err := d.Decode(b[:n])
		if err != nil {
			t.Fatalf("frame %d: %v", len(out), err)
		}
		out = append(out, f)
		b = b[n:]
	}
	return out
}

// The decoder's output is the reference's, sample for sample: ffmpeg's in
// its bit-exact (fixed point) mode, on streams ffmpeg's encoder makes.
func TestBitExactWithFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	for _, tc := range []struct {
		name, layout string
		order        []int // ffmpeg's channels as our speakers
	}{
		{"mono", "mono", []int{SpeakerC}},
		{"stereo", "stereo", []int{SpeakerL, SpeakerR}},
		{"5.1", "5.1(side)", []int{SpeakerL, SpeakerR, SpeakerC, SpeakerLFE, SpeakerLs, SpeakerRs}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x.dts")
			src := "sine=f=440:r=48000:d=2,aformat=channel_layouts=" + tc.layout
			if out, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-f", "lavfi", "-i", "aevalsrc=sin(440*2*PI*t)*0.5|sin(660*2*PI*t)*0.4|sin(880*2*PI*t)*0.3|sin(50*2*PI*t)*0.5|sin(1100*2*PI*t)*0.3|sin(1320*2*PI*t)*0.2:s=48000:d=2", //nolint:gosec // test
				"-af", "aformat=channel_layouts="+tc.layout, "-c:a", "dca", "-strict", "-2", "-f", "dts", path).CombinedOutput(); err != nil {
				_ = src
				t.Skipf("making DTS: %v %s", err, out)
			}
			ref, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-flags", "bitexact", "-f", "dts", "-i", path, "-f", "s32le", "-c:a", "pcm_s32le", "-").Output() //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(path) //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			frames := decodeAll(t, b)
			nch := len(tc.order)
			i := 0
			for fi, f := range frames {
				n := len(f.Samples[tc.order[0]])
				for k := range n {
					for c, spk := range tc.order {
						if 4*(i*nch+c)+4 > len(ref) {
							t.Fatalf("more samples than ffmpeg's (%d)", len(ref)/4/nch)
						}
						want := int32(binary.LittleEndian.Uint32(ref[4*(i*nch+c):])) //nolint:gosec // a sample
						if got := f.Samples[spk][k] << 8; got != want {
							t.Fatalf("frame %d sample %d channel %d: %d, ffmpeg %d", fi, k, c, got>>8, want>>8)
						}
					}
					i++
				}
			}
			if i*nch*4 != len(ref) {
				t.Errorf("%d samples, ffmpeg %d", i, len(ref)/4/nch)
			}
			t.Logf("%d frames, %d samples a channel, bit for bit", len(frames), i)
		})
	}
}

// TestFileBitExact compares the core of the DTS (or DTS-HD) stream in
// $BDTOOLS_DTS with ffmpeg's bit-exact decoding of its core.
func TestFileBitExact(t *testing.T) {
	path := os.Getenv("BDTOOLS_DTS")
	if path == "" {
		t.Skip("BDTOOLS_DTS not set")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	b, err := os.ReadFile(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	ref, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-flags", "bitexact", "-core_only", "1", "-f", "dts", "-i", path, "-f", "s32le", "-c:a", "pcm_s32le", "-").Output() //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	var d Decoder
	order := []int{SpeakerL, SpeakerR, SpeakerC, SpeakerLFE, SpeakerLs, SpeakerRs}
	nch := len(order)
	i, frames := 0, 0
	for len(b) > 0 {
		n := FrameSize(b)
		if n == 0 {
			// The extension substream after a core: on to the next core.
			k := 1
			for k+4 <= len(b) && (b[k] != 0x7f || b[k+1] != 0xfe || b[k+2] != 0x80 || b[k+3] != 0x01) {
				k++
			}
			b = b[min(k, len(b)):]
			continue
		}
		if n > len(b) {
			break
		}
		f, err := d.Decode(b[:n])
		if err != nil {
			t.Fatalf("frame %d: %v", frames, err)
		}
		for k := range f.Samples[SpeakerL] {
			for c, spk := range order {
				if 4*(i*nch+c)+4 > len(ref) {
					t.Fatalf("frame %d: more samples than ffmpeg's", frames)
				}
				want := int32(binary.LittleEndian.Uint32(ref[4*(i*nch+c):])) //nolint:gosec // a sample
				if got := f.Samples[spk][k] << 8; got != want {
					t.Fatalf("frame %d sample %d channel %d: %d, ffmpeg %d", frames, k, c, got>>8, want>>8)
				}
			}
			i++
		}
		frames++
		b = b[n:]
	}
	t.Logf("%d frames, %d samples a channel, bit for bit (ffmpeg %d)", frames, i, len(ref)/4/nch)
}
