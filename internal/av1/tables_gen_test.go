package av1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// The expected values below are copied from the specification's text, not
// from the generated tables.

func TestSpecConstants(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"REFS_PER_FRAME", REFS_PER_FRAME, 7},
		{"MAX_TILE_AREA", MAX_TILE_AREA, 4096 * 2304},
		{"SUPERRES_SCALE_MASK", SUPERRES_SCALE_MASK, 16383},
		{"WARPEDMODEL_TRANS_CLAMP", WARPEDMODEL_TRANS_CLAMP, 1 << 23},
		{"BLOCK_INVALID", BLOCK_INVALID, 22},
		{"BLOCK_64X16", BLOCK_64X16, 21},
		{"UV_CFL_PRED", UV_CFL_PRED, 13},
		{"PAETH_PRED", PAETH_PRED, 12},
		{"NEW_NEWMV", NEW_NEWMV, 25},
		{"TX_64X16", TX_64X16, 18},
		{"H_FLIPADST", H_FLIPADST, 15},
		{"PARTITION_VERT_4", PARTITION_VERT_4, 9},
		{"NONE", NONE, -1},
		{"ALTREF_FRAME", ALTREF_FRAME, 7},
		{"OBU_SEQUENCE_HEADER", OBU_SEQUENCE_HEADER, 1},
		{"OBU_PADDING", OBU_PADDING, 15},
		{"RESTORE_SWITCHABLE", RESTORE_SWITCHABLE, 3},
		{"TX_SET_INTER_3", TX_SET_INTER_3, 3},
		{"COMPOUND_DISTANCE", COMPOUND_DISTANCE, 4},
		{"MV_JOINT_HNZVNZ", MV_JOINT_HNZVNZ, 3},
		{"WEDGE_OBLIQUE153", WEDGE_OBLIQUE153, 5},
		{"SINPI_4_9", SINPI_4_9, 3803},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestSpecSpotValues(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"Default_Scan_4x4", defaultScan4x4, [16]uint16{0, 1, 4, 8, 5, 2, 3, 6, 9, 12, 13, 10, 7, 11, 14, 15}},
		{"Default_Scan_32x32[2]", defaultScan32x32[2], uint16(32)},
		{"Default_Scan_32x32[1017]", defaultScan32x32[1017], uint16(1020)},
		{"Dc_Qlookup[0][3]", dcQlookup[0][3], uint16(9)},
		{"Ac_Qlookup[2][255]", acQlookup[2][255], uint16(29247)},
		{"Gaussian_Sequence[2]", gaussianSequence[2], int16(-180)},
		{"Gaussian_Sequence[2047]", gaussianSequence[2047], int16(-484)},
		{"Div_Lut[0]", divLut[0], uint16(16384)},
		{"Div_Lut[256]", divLut[256], uint16(8192)},
		{"Dr_Intra_Derivative[6]", drIntraDerivative[6], uint16(547)},
		{"Upscale_Filter[1]", upscaleFilter[1], [8]int16{0, 0, -1, 128, 2, -1, 0, 0}},
		{"Quantizer_Matrix[0][0][:4]", [4]uint8(quantizerMatrix[0][0][:4]), [4]uint8{32, 43, 73, 97}},
		{"Quantizer_Matrix[14][1][3343]", quantizerMatrix[14][1][QM_TOTAL_SIZE-1], uint8(32)},
		{"Partition_Subsize[0][1]", partitionSubsize[0][1], blockSize(BLOCK_INVALID)},
		{"Subsampled_Size[BLOCK_4X8][1][0]", subsampledSize[BLOCK_4X8][1][0], blockSize(BLOCK_INVALID)},
		{"Split_Tx_Size[12]", splitTxSize[12], txSize(TX_32X32)},
		{"Split_Tx_Size[13]", splitTxSize[13], txSize(TX_4X8)}, // after the spec's missing comma
		{"Mode_To_Txfm[UV_CFL_PRED]", modeToTxfm[UV_CFL_PRED], txType(DCT_DCT)},
		{"Block_Width[BLOCK_128X128]", blockWidth[BLOCK_128X128], uint8(128)},
		{"Block_Height[BLOCK_64X16]", blockHeight[BLOCK_64X16], uint8(16)},
		{"Transform_Row_Shift", transformRowShift, [TX_SIZES_ALL]uint8{0, 1, 2, 2, 2, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2}},
		{"Default_Mv_Joint_Cdf", defaultMvJointCdf, [MV_JOINTS + 1]uint16{4096, 11264, 19328, 32768, 0}},
		{"Default_Skip_Cdf[0]", defaultSkipCdf[0], [3]uint16{31671, 32768, 0}},
		{"Default_Delta_Q_Cdf", defaultDeltaQCdf, [DELTA_Q_SMALL + 2]uint16{28160, 32120, 32677, 32768, 0}},
		{"Default_Restoration_Type_Cdf", defaultRestorationTypeCdf, [RESTORE_SWITCHABLE + 1]uint16{9413, 22581, 32768, 0}},
		{"Default_Intra_Frame_Y_Mode_Cdf[0][0]", defaultIntraFrameYModeCdf[0][0], [INTRA_MODES + 1]uint16{15588, 17027, 19338, 20218, 20682, 21110, 21825, 23244, 24189, 28165, 29093, 30466, 32768, 0}},
		{"Default_Eob_Pt_1024_Cdf[0][1]", defaultEobPt1024Cdf[0][1], [12]uint16{1865, 1988, 2930, 4242, 10533, 16538, 21354, 27255, 28546, 31784, 32768, 0}},
		{"Default_Coeff_Br_Cdf[0][0][0][0]", defaultCoeffBrCdf[0][0][0][0], [BR_CDF_SIZE + 1]uint16{14298, 20718, 24174, 32768, 0}},
		{"Default_Coeff_Br_Cdf[3][4][1][20]", defaultCoeffBrCdf[3][4][1][20], [BR_CDF_SIZE + 1]uint16{8192, 16384, 24576, 32768, 0}},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// defaultCdfs lists every Default_*_Cdf table; TestCdfListComplete keeps
// it in step with tables_gen.go.
var defaultCdfs = map[string]any{
	"defaultIntraFrameYModeCdf":     defaultIntraFrameYModeCdf,
	"defaultYModeCdf":               defaultYModeCdf,
	"defaultUvModeCflNotAllowedCdf": defaultUvModeCflNotAllowedCdf,
	"defaultUvModeCflAllowedCdf":    defaultUvModeCflAllowedCdf,
	"defaultAngleDeltaCdf":          defaultAngleDeltaCdf,
	"defaultIntrabcCdf":             defaultIntrabcCdf,
	"defaultPartitionW8Cdf":         defaultPartitionW8Cdf,
	"defaultPartitionW16Cdf":        defaultPartitionW16Cdf,
	"defaultPartitionW32Cdf":        defaultPartitionW32Cdf,
	"defaultPartitionW64Cdf":        defaultPartitionW64Cdf,
	"defaultPartitionW128Cdf":       defaultPartitionW128Cdf,
	"defaultTx8x8Cdf":               defaultTx8x8Cdf,
	"defaultTx16x16Cdf":             defaultTx16x16Cdf,
	"defaultTx32x32Cdf":             defaultTx32x32Cdf,
	"defaultTx64x64Cdf":             defaultTx64x64Cdf,
	"defaultTxfmSplitCdf":           defaultTxfmSplitCdf,
	"defaultFilterIntraModeCdf":     defaultFilterIntraModeCdf,
	"defaultFilterIntraCdf":         defaultFilterIntraCdf,
	"defaultSegmentIdCdf":           defaultSegmentIdCdf,
	"defaultSegmentIdPredictedCdf":  defaultSegmentIdPredictedCdf,
	"defaultMvClass0HpCdf":          defaultMvClass0HpCdf,
	"defaultMvHpCdf":                defaultMvHpCdf,
	"defaultMvSignCdf":              defaultMvSignCdf,
	"defaultMvBitCdf":               defaultMvBitCdf,
	"defaultMvClass0BitCdf":         defaultMvClass0BitCdf,
	"defaultNewMvCdf":               defaultNewMvCdf,
	"defaultZeroMvCdf":              defaultZeroMvCdf,
	"defaultRefMvCdf":               defaultRefMvCdf,
	"defaultDrlModeCdf":             defaultDrlModeCdf,
	"defaultIsInterCdf":             defaultIsInterCdf,
	"defaultCompModeCdf":            defaultCompModeCdf,
	"defaultSkipModeCdf":            defaultSkipModeCdf,
	"defaultSkipCdf":                defaultSkipCdf,
	"defaultCompRefCdf":             defaultCompRefCdf,
	"defaultCompBwdRefCdf":          defaultCompBwdRefCdf,
	"defaultSingleRefCdf":           defaultSingleRefCdf,
	"defaultCompoundModeCdf":        defaultCompoundModeCdf,
	"defaultInterpFilterCdf":        defaultInterpFilterCdf,
	"defaultMotionModeCdf":          defaultMotionModeCdf,
	"defaultMvJointCdf":             defaultMvJointCdf,
	"defaultMvClassCdf":             defaultMvClassCdf,
	"defaultMvClass0FrCdf":          defaultMvClass0FrCdf,
	"defaultMvFrCdf":                defaultMvFrCdf,
	"defaultPaletteYSizeCdf":        defaultPaletteYSizeCdf,
	"defaultPaletteUvSizeCdf":       defaultPaletteUvSizeCdf,
	"defaultPaletteSize2YColorCdf":  defaultPaletteSize2YColorCdf,
	"defaultPaletteSize3YColorCdf":  defaultPaletteSize3YColorCdf,
	"defaultPaletteSize4YColorCdf":  defaultPaletteSize4YColorCdf,
	"defaultPaletteSize5YColorCdf":  defaultPaletteSize5YColorCdf,
	"defaultPaletteSize6YColorCdf":  defaultPaletteSize6YColorCdf,
	"defaultPaletteSize7YColorCdf":  defaultPaletteSize7YColorCdf,
	"defaultPaletteSize8YColorCdf":  defaultPaletteSize8YColorCdf,
	"defaultPaletteSize2UvColorCdf": defaultPaletteSize2UvColorCdf,
	"defaultPaletteSize3UvColorCdf": defaultPaletteSize3UvColorCdf,
	"defaultPaletteSize4UvColorCdf": defaultPaletteSize4UvColorCdf,
	"defaultPaletteSize5UvColorCdf": defaultPaletteSize5UvColorCdf,
	"defaultPaletteSize6UvColorCdf": defaultPaletteSize6UvColorCdf,
	"defaultPaletteSize7UvColorCdf": defaultPaletteSize7UvColorCdf,
	"defaultPaletteSize8UvColorCdf": defaultPaletteSize8UvColorCdf,
	"defaultPaletteYModeCdf":        defaultPaletteYModeCdf,
	"defaultPaletteUvModeCdf":       defaultPaletteUvModeCdf,
	"defaultDeltaQCdf":              defaultDeltaQCdf,
	"defaultDeltaLfCdf":             defaultDeltaLfCdf,
	"defaultIntraTxTypeSet1Cdf":     defaultIntraTxTypeSet1Cdf,
	"defaultIntraTxTypeSet2Cdf":     defaultIntraTxTypeSet2Cdf,
	"defaultInterTxTypeSet1Cdf":     defaultInterTxTypeSet1Cdf,
	"defaultInterTxTypeSet2Cdf":     defaultInterTxTypeSet2Cdf,
	"defaultInterTxTypeSet3Cdf":     defaultInterTxTypeSet3Cdf,
	"defaultCompoundIdxCdf":         defaultCompoundIdxCdf,
	"defaultCompGroupIdxCdf":        defaultCompGroupIdxCdf,
	"defaultCompoundTypeCdf":        defaultCompoundTypeCdf,
	"defaultInterIntraCdf":          defaultInterIntraCdf,
	"defaultInterIntraModeCdf":      defaultInterIntraModeCdf,
	"defaultWedgeIndexCdf":          defaultWedgeIndexCdf,
	"defaultWedgeInterIntraCdf":     defaultWedgeInterIntraCdf,
	"defaultUseObmcCdf":             defaultUseObmcCdf,
	"defaultCompRefTypeCdf":         defaultCompRefTypeCdf,
	"defaultUniCompRefCdf":          defaultUniCompRefCdf,
	"defaultCflSignCdf":             defaultCflSignCdf,
	"defaultCflAlphaCdf":            defaultCflAlphaCdf,
	"defaultUseWienerCdf":           defaultUseWienerCdf,
	"defaultUseSgrprojCdf":          defaultUseSgrprojCdf,
	"defaultRestorationTypeCdf":     defaultRestorationTypeCdf,
	"defaultTxbSkipCdf":             defaultTxbSkipCdf,
	"defaultEobPt16Cdf":             defaultEobPt16Cdf,
	"defaultEobPt32Cdf":             defaultEobPt32Cdf,
	"defaultEobPt64Cdf":             defaultEobPt64Cdf,
	"defaultEobPt128Cdf":            defaultEobPt128Cdf,
	"defaultEobPt256Cdf":            defaultEobPt256Cdf,
	"defaultEobPt512Cdf":            defaultEobPt512Cdf,
	"defaultEobPt1024Cdf":           defaultEobPt1024Cdf,
	"defaultEobExtraCdf":            defaultEobExtraCdf,
	"defaultDcSignCdf":              defaultDcSignCdf,
	"defaultCoeffBaseEobCdf":        defaultCoeffBaseEobCdf,
	"defaultCoeffBaseCdf":           defaultCoeffBaseCdf,
	"defaultCoeffBrCdf":             defaultCoeffBrCdf,
}

func TestCdfListComplete(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "tables_gen.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, s := range gd.Specs {
			for _, id := range s.(*ast.ValueSpec).Names {
				if strings.HasSuffix(id.Name, "Cdf") {
					n++
					if _, ok := defaultCdfs[id.Name]; !ok {
						t.Errorf("%s missing from defaultCdfs", id.Name)
					}
				}
			}
		}
	}
	if n != len(defaultCdfs) {
		t.Errorf("tables_gen.go has %d CDF tables, defaultCdfs %d", n, len(defaultCdfs))
	}
}

// TestCdfRows checks every CDF row: non-decreasing, then 32768 and the
// adaptation count 0.
func TestCdfRows(t *testing.T) {
	rows := 0
	for name, tab := range defaultCdfs {
		var walk func(v reflect.Value, path string)
		walk = func(v reflect.Value, path string) {
			if v.Type().Elem().Kind() == reflect.Array {
				for i := range v.Len() {
					walk(v.Index(i), path+"["+itoa(i)+"]")
				}
				return
			}
			rows++
			n := v.Len()
			if n < 3 || v.Index(n-2).Uint() != 32768 || v.Index(n-1).Uint() != 0 {
				t.Errorf("%s: row %v does not end with 32768, 0", path, v.Interface())
				return
			}
			for i := 1; i < n-1; i++ {
				if v.Index(i).Uint() < v.Index(i-1).Uint() {
					t.Errorf("%s: row %v decreases", path, v.Interface())
				}
			}
		}
		walk(reflect.ValueOf(tab), name)
	}
	if rows == 0 {
		t.Fatal("no CDF rows")
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

// TestScans checks each scan is a permutation, and the transposes listed
// in the spec's scan tables section.
func TestScans(t *testing.T) {
	type scan struct {
		s    []uint16
		w, h int
	}
	scans := map[string]scan{
		"Default_Scan_4x4": {defaultScan4x4[:], 4, 4}, "Mcol_Scan_4x4": {mcolScan4x4[:], 4, 4}, "Mrow_Scan_4x4": {mrowScan4x4[:], 4, 4},
		"Default_Scan_4x8": {defaultScan4x8[:], 4, 8}, "Mcol_Scan_4x8": {mcolScan4x8[:], 4, 8}, "Mrow_Scan_4x8": {mrowScan4x8[:], 4, 8},
		"Default_Scan_8x4": {defaultScan8x4[:], 8, 4}, "Mcol_Scan_8x4": {mcolScan8x4[:], 8, 4}, "Mrow_Scan_8x4": {mrowScan8x4[:], 8, 4},
		"Default_Scan_8x8": {defaultScan8x8[:], 8, 8}, "Mcol_Scan_8x8": {mcolScan8x8[:], 8, 8}, "Mrow_Scan_8x8": {mrowScan8x8[:], 8, 8},
		"Default_Scan_8x16": {defaultScan8x16[:], 8, 16}, "Mcol_Scan_8x16": {mcolScan8x16[:], 8, 16}, "Mrow_Scan_8x16": {mrowScan8x16[:], 8, 16},
		"Default_Scan_16x8": {defaultScan16x8[:], 16, 8}, "Mcol_Scan_16x8": {mcolScan16x8[:], 16, 8}, "Mrow_Scan_16x8": {mrowScan16x8[:], 16, 8},
		"Default_Scan_16x16": {defaultScan16x16[:], 16, 16}, "Mcol_Scan_16x16": {mcolScan16x16[:], 16, 16}, "Mrow_Scan_16x16": {mrowScan16x16[:], 16, 16},
		"Default_Scan_16x32": {defaultScan16x32[:], 16, 32}, "Default_Scan_32x16": {defaultScan32x16[:], 32, 16}, "Default_Scan_32x32": {defaultScan32x32[:], 32, 32},
		"Default_Scan_4x16": {defaultScan4x16[:], 4, 16}, "Mcol_Scan_4x16": {mcolScan4x16[:], 4, 16}, "Mrow_Scan_4x16": {mrowScan4x16[:], 4, 16},
		"Default_Scan_16x4": {defaultScan16x4[:], 16, 4}, "Mcol_Scan_16x4": {mcolScan16x4[:], 16, 4}, "Mrow_Scan_16x4": {mrowScan16x4[:], 16, 4},
		"Default_Scan_8x32": {defaultScan8x32[:], 8, 32}, "Default_Scan_32x8": {defaultScan32x8[:], 32, 8},
	}
	for name, s := range scans {
		seen := make([]bool, s.w*s.h)
		for _, p := range s.s {
			if int(p) >= len(seen) || seen[p] {
				t.Errorf("%s is not a permutation of 0..%d", name, len(seen)-1)
				break
			}
			seen[p] = true
		}
	}
	for _, pair := range [][2]string{
		{"Default_Scan_4x8", "Default_Scan_8x4"}, {"Default_Scan_16x32", "Default_Scan_32x16"},
		{"Default_Scan_16x4", "Default_Scan_4x16"}, {"Default_Scan_16x8", "Default_Scan_8x16"},
		{"Default_Scan_32x8", "Default_Scan_8x32"}, {"Mcol_Scan_4x4", "Mrow_Scan_4x4"},
		{"Mcol_Scan_4x8", "Mrow_Scan_8x4"}, {"Mcol_Scan_8x8", "Mrow_Scan_8x8"},
		{"Mcol_Scan_16x8", "Mrow_Scan_8x16"}, {"Mcol_Scan_16x16", "Mrow_Scan_16x16"},
		{"Mcol_Scan_16x4", "Mrow_Scan_4x16"}, {"Mrow_Scan_4x4", "Mcol_Scan_4x4"},
		{"Mrow_Scan_4x8", "Mcol_Scan_8x4"}, {"Mrow_Scan_8x8", "Mcol_Scan_8x8"},
		{"Mrow_Scan_16x8", "Mcol_Scan_8x16"}, {"Mrow_Scan_16x16", "Mcol_Scan_16x16"},
		{"Mrow_Scan_16x4", "Mcol_Scan_4x16"},
	} {
		a, b := scans[pair[0]], scans[pair[1]]
		w, h := a.w, a.h
		for pos := range w * h {
			x1, y1 := int(a.s[pos])%w, int(a.s[pos])/w
			x2, y2 := int(b.s[pos])%h, int(b.s[pos])/h
			if x1 != y2 || y1 != x2 {
				t.Errorf("%s is not the transpose of %s (position %d)", pair[0], pair[1], pos)
				break
			}
		}
	}
}
