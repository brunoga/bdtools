package hevc

// Pack writes rows [r0, r1) of the picture (r0 even) as an encoder takes
// it: NV12 at 8 bits, else P010 (two bytes a sample, little-endian, the
// value in the top bits), a luma plane y and an interleaved Cb/Cr plane uv,
// each row pitch bytes apart. Rows may be packed in bands concurrently.
func (p *Picture) Pack(y, uv []byte, pitch, r0, r1 int) {
	w := p.Width
	cw, ch := (w+1)/2, (p.Height+1)/2
	shift := uint(16 - p.BitDepth)
	for r := r0; r < r1; r++ {
		src := p.Y[r*p.StrideY : r*p.StrideY+w]
		if p.BitDepth == 8 {
			packBytes(y[r*pitch:r*pitch+w], src)
		} else {
			packWords(y[r*pitch:r*pitch+2*w], src, shift)
		}
	}
	for r := r0 / 2; r < min(ch, (r1+1)/2); r++ {
		cb, cr := p.Cb[r*p.StrideC:r*p.StrideC+cw], p.Cr[r*p.StrideC:r*p.StrideC+cw]
		if p.BitDepth == 8 {
			weaveBytes(uv[r*pitch:r*pitch+2*cw], cb, cr)
		} else {
			weaveWords(uv[r*pitch:r*pitch+4*cw], cb, cr, shift)
		}
	}
}

func packBytesGo(dst []byte, src []uint16) {
	for x, v := range src {
		dst[x] = byte(v)
	}
}

func packWordsGo(dst []byte, src []uint16, shift uint) {
	for x, v := range src {
		v <<= shift
		dst[2*x], dst[2*x+1] = byte(v), byte(v>>8)
	}
}

func weaveBytesGo(dst []byte, cb, cr []uint16) {
	for x := range cb {
		dst[2*x], dst[2*x+1] = byte(cb[x]), byte(cr[x])
	}
}

func weaveWordsGo(dst []byte, cb, cr []uint16, shift uint) {
	for x := range cb {
		b, r := cb[x]<<shift, cr[x]<<shift
		dst[4*x], dst[4*x+1], dst[4*x+2], dst[4*x+3] = byte(b), byte(b>>8), byte(r), byte(r>>8)
	}
}
