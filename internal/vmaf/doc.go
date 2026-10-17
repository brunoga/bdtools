// Package vmaf measures a distorted video against its reference as
// Netflix's VMAF does: libvmaf's integer feature extractors (VIF at four
// scales, ADM, motion) ported to Go, and its v0.6.1 models (the 1080p
// one, and the 4K one for pictures above 1080 lines). The ports follow
// libvmaf's arithmetic step for step, so the features and the score are
// the ones its vmaf tool gives.
//
// libvmaf is Netflix's, under the BSD+Patent license, a copy of which is
// in LICENSE.libvmaf.
package vmaf

// Frame is a picture's luma: W x H samples of BPC bits (8 to 16), one
// row after another.
type Frame struct {
	W, H, BPC int
	Y         []uint16
}
