//go:build linux && (amd64 || arm64)

package gpu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brunoga/bdtools/mvc"
)

// VAAPI decodes the 2D H.264 conformance streams (testdata/conformance/2d,
// those the decoder here decodes: progressive) as the decoder here does,
// picture for picture.
func TestVAAPIH264Conformance(t *testing.T) {
	probe, err := openVAAPIDecoder(DecodeConfig{Codec: DecodeH264}, func(*DecodedPicture) error { return nil })
	if err != nil {
		t.Skip(err)
	}
	// Closed before the others open: the driver leaks what decoders closed
	// while another display stays open hold, until decoding fails.
	_ = probe.Close()
	paths, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "conformance", "2d", "*.264"))
	if len(paths) == 0 {
		t.Skip("no conformance streams")
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".264")
		t.Run(name, func(t *testing.T) {
			if name == "MR4_TANDBERG_C" {
				// Gaps in frame_num, memory management control operation 5
				// and long-term references at once (never on a disc): a
				// tenth of its pictures come out of Intel's driver
				// otherwise than ffmpeg's VAAPI decode has them, the
				// parameters given alike but for the surfaces chosen.
				t.Skip("decoded otherwise by Intel's driver")
			}
			b, err := os.ReadFile(path) //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			// The decoder here, its pictures as NV12.
			var want []string
			sw := mvc.NewDecoder(mvc.Options{BaseOnly: true})
			take := func() {
				for {
					f, ok := sw.WaitFrame()
					if !ok {
						return
					}
					p := f.Base
					var nv []byte
					for y := range p.Height {
						nv = append(nv, p.Y[y*p.StrideY:y*p.StrideY+p.Width]...)
					}
					for y := range p.Height / 2 {
						for x := range p.Width / 2 {
							nv = append(nv, p.Cb[y*p.StrideC+x], p.Cr[y*p.StrideC+x])
						}
					}
					want = append(want, string(nv))
					f.Release()
				}
			}
			if err := sw.DecodeAU(b, 0); err != nil {
				t.Skipf("the decoder here: %v", err)
			}
			take()
			if err := sw.Flush(); err != nil {
				t.Skipf("the decoder here: %v", err)
			}
			take()
			var got []string
			d, err := OpenDecoder(VAAPI, DecodeConfig{Codec: DecodeH264}, func(p *DecodedPicture) error {
				var nv []byte
				for y := range p.Height {
					nv = append(nv, p.Y[y*p.Pitch:y*p.Pitch+p.Width]...)
				}
				for y := range p.Height / 2 {
					nv = append(nv, p.UV[y*p.Pitch:y*p.Pitch+p.Width]...)
				}
				got = append(got, string(nv))
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			for _, u := range annexBUnits(b, false) {
				if err := d.Decode(u, 0); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.Flush(); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("%d pictures, the decoder here %d", len(got), len(want))
			}
			bad := 0
			for i := range got {
				if got[i] != want[i] {
					bad++
				}
			}
			if bad > 0 {
				t.Fatalf("%d of %d pictures differ", bad, len(got))
			}
		})
	}
}
