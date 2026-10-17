package av1

// Scans (get_scan): the order a transform block's coefficients are coded
// in, by its size and its type's class.

var defaultScans = [TX_SIZES_ALL][]uint16{
	TX_4X4: defaultScan4x4[:], TX_8X8: defaultScan8x8[:], TX_16X16: defaultScan16x16[:], TX_32X32: defaultScan32x32[:],
	TX_64X64: defaultScan32x32[:], TX_4X8: defaultScan4x8[:], TX_8X4: defaultScan8x4[:], TX_8X16: defaultScan8x16[:],
	TX_16X8: defaultScan16x8[:], TX_16X32: defaultScan16x32[:], TX_32X16: defaultScan32x16[:], TX_32X64: defaultScan32x32[:],
	TX_64X32: defaultScan32x32[:], TX_4X16: defaultScan4x16[:], TX_16X4: defaultScan16x4[:], TX_8X32: defaultScan8x32[:],
	TX_32X8: defaultScan32x8[:], TX_16X64: defaultScan16x32[:], TX_64X16: defaultScan32x16[:],
}

var mrowScans = [TX_SIZES_ALL][]uint16{
	TX_4X4: mrowScan4x4[:], TX_4X8: mrowScan4x8[:], TX_8X4: mrowScan8x4[:], TX_8X8: mrowScan8x8[:],
	TX_8X16: mrowScan8x16[:], TX_16X8: mrowScan16x8[:], TX_16X16: mrowScan16x16[:], TX_4X16: mrowScan4x16[:], TX_16X4: mrowScan16x4[:],
}

var mcolScans = [TX_SIZES_ALL][]uint16{
	TX_4X4: mcolScan4x4[:], TX_4X8: mcolScan4x8[:], TX_8X4: mcolScan8x4[:], TX_8X8: mcolScan8x8[:],
	TX_8X16: mcolScan8x16[:], TX_16X8: mcolScan16x8[:], TX_16X16: mcolScan16x16[:], TX_4X16: mcolScan4x16[:], TX_16X4: mcolScan16x4[:],
}

// txClass is get_tx_class.
func txClass(t int) int {
	switch t {
	case V_DCT, V_ADST, V_FLIPADST:
		return TX_CLASS_VERT
	case H_DCT, H_ADST, H_FLIPADST:
		return TX_CLASS_HORIZ
	}
	return TX_CLASS_2D
}

// scanFor is get_scan.
func scanFor(txSz, txType int) []uint16 {
	if txSizeSqrUp[txSz] == TX_64X64 || txType == IDTX {
		return defaultScans[txSz]
	}
	switch txClass(txType) {
	case TX_CLASS_VERT:
		return mrowScans[txSz]
	case TX_CLASS_HORIZ:
		return mcolScans[txSz]
	}
	return defaultScans[txSz]
}
