//go:build linux && (amd64 || arm64)

package gpu

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// The Go mirrors of the decoding buffers lie as the C headers do
// (the numbers come from them: TestVAAPILayout).
func TestVAAPIDecodeLayout(t *testing.T) {
	var p vaPicParamHEVC
	var s vaSliceParamHEVC
	var q vaIQMatrixHEVC
	var mp vaPicParamMPEG2
	var mq vaIQMatrixMPEG2
	var ms vaSliceParamMPEG2
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"picture size", unsafe.Sizeof(p), vaSizeHEVCDecPic},
		{"ReferenceFrames", unsafe.Offsetof(p.Refs), vaHEVCDecPicRefs},
		{"pic_width_in_luma_samples", unsafe.Offsetof(p.Width), vaHEVCDecPicWidth},
		{"pic_fields", unsafe.Offsetof(p.PicFields), vaHEVCDecPicFields},
		{"sps_max_dec_pic_buffering_minus1", unsafe.Offsetof(p.MaxDecPicBufferingMinus1), vaHEVCDecPicMaxDecPicBuf},
		{"init_qp_minus26", unsafe.Offsetof(p.InitQPMinus26), vaHEVCDecPicInitQP},
		{"num_tile_rows_minus1", unsafe.Offsetof(p.NumTileRowsMinus1), vaHEVCDecPicTileRows},
		{"column_width_minus1", unsafe.Offsetof(p.ColumnWidthMinus1), vaHEVCDecPicColumnWidths},
		{"row_height_minus1", unsafe.Offsetof(p.RowHeightMinus1), vaHEVCDecPicRowHeights},
		{"slice_parsing_fields", unsafe.Offsetof(p.SliceParsingFields), vaHEVCDecPicSliceFields},
		{"log2_max_pic_order_cnt_lsb_minus4", unsafe.Offsetof(p.Log2MaxPOCLsbMinus4), vaHEVCDecPicLog2MaxPOCLsb},
		{"num_extra_slice_header_bits", unsafe.Offsetof(p.NumExtraSliceHeaderBits), vaHEVCDecPicExtraBits},
		{"st_rps_bits", unsafe.Offsetof(p.StRPSBits), vaHEVCDecPicStRPSBits},
		{"slice size", unsafe.Sizeof(s), vaSizeHEVCDecSlice},
		{"RefPicList", unsafe.Offsetof(s.RefPicList), vaHEVCDecSliceRefs},
		{"LongSliceFlags", unsafe.Offsetof(s.LongSliceFlags), vaHEVCDecSliceFlags},
		{"collocated_ref_idx", unsafe.Offsetof(s.CollocatedRefIdx), vaHEVCDecSliceColRefIdx},
		{"delta_chroma_log2_weight_denom", unsafe.Offsetof(s.DeltaChromaLog2WeightDenom), vaHEVCDecSliceChromaDenom},
		{"delta_luma_weight_l0", unsafe.Offsetof(s.Weights), vaHEVCDecSliceWeightsL0},
		{"ChromaOffsetL1", unsafe.Offsetof(s.Weights) + unsafe.Sizeof(s.Weights[0]) + unsafe.Offsetof(s.Weights[0].ChromaOffset), vaHEVCDecSliceChromaOffsetL1},
		{"five_minus_max_num_merge_cand", unsafe.Offsetof(s.FiveMinusMaxNumMergeCand), vaHEVCDecSliceMaxMerge},
		{"num_entry_point_offsets", unsafe.Offsetof(s.NumEntryPointOffsets), vaHEVCDecSliceEntryPoints},
		{"slice_data_num_emu_prevn_bytes", unsafe.Offsetof(s.SliceDataNumEmuPrevnBytes), vaHEVCDecSliceEmuBytes},
		{"IQ size", unsafe.Sizeof(q), vaSizeHEVCIQ},
		{"ScalingList32x32", unsafe.Offsetof(q.L32), vaHEVCIQ32},
		{"ScalingListDC32x32", unsafe.Offsetof(q.DC32), vaHEVCIQDC32},
		{"MPEG-2 picture size", unsafe.Sizeof(mp), vaSizeMPEG2Pic},
		{"forward_reference_picture", unsafe.Offsetof(mp.Forward), vaMPEG2PicForward},
		{"picture_coding_type", unsafe.Offsetof(mp.CodingType), vaMPEG2PicType},
		{"picture_coding_extension", unsafe.Offsetof(mp.Ext), vaMPEG2PicExt},
		{"MPEG-2 IQ size", unsafe.Sizeof(mq), vaSizeMPEG2IQ},
		{"intra_quantiser_matrix", unsafe.Offsetof(mq.Intra), vaMPEG2IQIntra},
		{"chroma_non_intra_quantiser_matrix", unsafe.Offsetof(mq.ChromaNonIntra), vaMPEG2IQChromaNonIntra},
		{"MPEG-2 slice size", unsafe.Sizeof(ms), vaSizeMPEG2Slice},
		{"macroblock_offset", unsafe.Offsetof(ms.MBOffset), vaMPEG2SliceMBOffset},
		{"intra_slice_flag", unsafe.Offsetof(ms.IntraSlice), vaMPEG2SliceIntra},
	} {
		if c.got != c.want {
			t.Errorf("%s at %d, C's at %d", c.name, c.got, c.want)
		}
	}
}

// VAAPI decodes the JCT-VC conformance streams in $BDTOOLS_HEVC_SAMPLES
// ($BDTOOLS_HEVC_ONLY: a glob of names) as ffmpeg does.
func TestVAAPIConformance(t *testing.T) {
	dir := os.Getenv("BDTOOLS_HEVC_SAMPLES")
	if dir == "" {
		t.Skip("BDTOOLS_HEVC_SAMPLES not set")
	}
	glob := os.Getenv("BDTOOLS_HEVC_ONLY")
	if glob == "" {
		glob = "*"
	}
	paths, _ := filepath.Glob(filepath.Join(dir, glob+".bit"))
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if strings.Contains(path, "RExt") {
				t.Skip("the format range extensions")
			}
			b, err := os.ReadFile(path) //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			units := annexBUnits(b, true)
			pts := make([]int64, len(units))
			got, _, last := decodeAll(t, VAAPI, DecodeHEVC, units, pts)
			if len(got) == 0 {
				t.Fatal("no pictures")
			}
			pix := "nv12"
			if last.Depth > 8 {
				pix = "p010le"
			}
			out, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-flags", "+output_corrupt", "-f", "hevc", "-i", path, //nolint:gosec // test
				"-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", pix, "-").Output()
			if err != nil {
				t.Fatal(err)
			}
			size := len(got[0])
			if len(out) != size*len(got) {
				t.Fatalf("%d pictures, ffmpeg %d", len(got), len(out)/size)
			}
			for i := range got {
				if got[i] != string(out[i*size:(i+1)*size]) {
					t.Fatalf("picture %d of %d differs from ffmpeg's", i, len(got))
				}
			}
		})
	}
}

// BenchmarkVAAPIDecode decodes the HEVC stream in $BDTOOLS_HEVC_BENCH
// (Annex B), the pictures copied out as the converter takes them.
func BenchmarkVAAPIDecode(b *testing.B) {
	path := os.Getenv("BDTOOLS_HEVC_BENCH")
	if path == "" {
		b.Skip("BDTOOLS_HEVC_BENCH not set")
	}
	data, err := os.ReadFile(path) //nolint:gosec // test
	if err != nil {
		b.Fatal(err)
	}
	units := annexBUnits(data, true)
	n := 0
	start := time.Now()
	for b.Loop() {
		d, err := OpenDecoder(VAAPI, DecodeConfig{Codec: DecodeHEVC}, func(*DecodedPicture) error { n++; return nil })
		if err != nil {
			b.Skip(err)
		}
		for _, u := range units {
			if err := d.Decode(u, 0); err != nil {
				b.Fatal(err)
			}
		}
		if err := d.Flush(); err != nil {
			b.Fatal(err)
		}
		_ = d.Close()
	}
	b.ReportMetric(float64(n)/time.Since(start).Seconds(), "pictures/s")
}
