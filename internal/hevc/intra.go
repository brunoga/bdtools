package hevc

// intraPredict predicts the nTbS = 1 << log2Size block of component ci at
// (x0, y0) (the component's samples) with mode (8.4.4.2).
func (sd *sliceDec) intraPredict(x0, y0, log2Size, ci, mode int) {
	s, p := sd.s, sd.p
	n := 1 << log2Size
	pl, stride := sd.pic.y, sd.pic.strideY
	depth := s.bitDepth
	shift := 0 // component to luma coordinates
	if ci > 0 {
		pl, stride = sd.pic.cb, sd.pic.strideC
		if ci == 2 {
			pl = sd.pic.cr
		}
		depth = s.bitDepthC
		shift = 1
	}
	// The references in substitution order: p[-1][2n-1] up to p[-1][-1],
	// then p[0][-1] to p[2n-1][-1] (8.4.4.2.2).
	var refBuf, availBuf [4*64 + 1]int32
	ref := refBuf[:4*n+1]
	avail := availBuf[:4*n+1]
	xL, yL := x0<<shift, y0<<shift // the block in luma samples
	unit := 4 >> shift             // component samples a 4x4 luma block covers
	ok := func(xc, yc int) bool {
		xn, yn := xc<<shift, yc<<shift
		if !sd.available(xL, yL, xn, yn) {
			return false
		}
		return !p.constrainedIntraPred || sd.ps.predMode[sd.blk4(xn, yn)] == modeIntra
	}
	any := false
	// Left column, bottom up.
	for y := 2*n - 1; y >= 0; y -= unit {
		a := ok(x0-1, y0+y-unit+1)
		for k := range unit {
			i := 2*n - 1 - (y - k)
			if a {
				ref[i] = int32(pl[(y0+y-k)*stride+x0-1])
				avail[i] = 1
				any = true
			} else {
				avail[i] = 0
			}
		}
	}
	if ok(x0-1, y0-1) {
		ref[2*n] = int32(pl[(y0-1)*stride+x0-1])
		avail[2*n] = 1
		any = true
	} else {
		avail[2*n] = 0
	}
	for x := 0; x < 2*n; x += unit {
		a := ok(x0+x, y0-1)
		for k := range unit {
			i := 2*n + 1 + x + k
			if a {
				ref[i] = int32(pl[(y0-1)*stride+x0+x+k])
				avail[i] = 1
				any = true
			} else {
				avail[i] = 0
			}
		}
	}
	if !any {
		mid := int32(1) << (depth - 1)
		for i := range ref {
			ref[i] = mid
		}
	} else {
		if avail[0] == 0 {
			for i := 1; i < len(ref); i++ {
				if avail[i] != 0 {
					ref[0] = ref[i]
					break
				}
			}
		}
		for i := 1; i < len(ref); i++ {
			if avail[i] == 0 {
				ref[i] = ref[i-1]
			}
		}
	}
	// Filtering (8.4.4.2.3), luma only for 4:2:0.
	if ci == 0 && mode != 1 && n != 4 {
		dist := min(iabs(mode-26), iabs(mode-10))
		thres := map[int]int{8: 7, 16: 1, 32: 0}[n]
		if dist > thres {
			var fBuf [4*64 + 1]int32
			f := fBuf[:len(ref)]
			corner := ref[2*n]
			bottom := ref[0]  // p[-1][2n-1]
			right := ref[4*n] // p[2n-1][-1]
			midL := ref[n]    // p[-1][n-1]
			midT := ref[3*n]  // p[n-1][-1]
			thr := int32(1) << (depth - 5)
			if s.strongIntraSmoothing && n == 32 && iabs32(corner+right-2*midT) < thr && iabs32(corner+bottom-2*midL) < thr {
				f[2*n] = corner
				for y := range 63 {
					// pF[-1][y], at index 2n-1-y.
					f[2*n-1-y] = ((63-int32(y))*corner + (int32(y)+1)*bottom + 32) >> 6
				}
				f[0] = bottom
				for x := range 63 {
					f[2*n+1+x] = ((63-int32(x))*corner + (int32(x)+1)*right + 32) >> 6
				}
				f[4*n] = right
			} else {
				f[0] = ref[0]
				f[4*n] = ref[4*n]
				for i := 1; i < 4*n; i++ {
					f[i] = (ref[i-1] + 2*ref[i] + ref[i+1] + 2) >> 2
				}
			}
			copy(ref, f)
		}
	}
	// left(y) is p[-1][y], top(x) p[x][-1], for -1 <= x, y < 2n.
	left := func(y int) int32 { return ref[2*n-1-y] }
	top := func(x int) int32 { return ref[2*n+1+x] }
	maxV := int32(1)<<depth - 1
	clip := func(v int32) int32 { return min(max(v, 0), maxV) }
	put := func(x, y int, v int32) { pl[(y0+y)*stride+x0+x] = uint16(v) }
	switch mode {
	case 0: // planar
		sh := uint(log2Size + 1)
		tr, bl := top(n), left(n)
		for y := range n {
			for x := range n {
				v := (int32(n-1-x)*left(y) + int32(x+1)*tr + int32(n-1-y)*top(x) + int32(y+1)*bl + int32(n)) >> sh
				put(x, y, v)
			}
		}
	case 1: // DC
		var sum int32
		for i := range n {
			sum += top(i) + left(i)
		}
		dc := (sum + int32(n)) >> uint(log2Size+1)
		for y := range n {
			for x := range n {
				put(x, y, dc)
			}
		}
		if ci == 0 && n < 32 {
			put(0, 0, (left(0)+2*dc+top(0)+2)>>2)
			for x := 1; x < n; x++ {
				put(x, 0, (top(x)+3*dc+2)>>2)
			}
			for y := 1; y < n; y++ {
				put(0, y, (left(y)+3*dc+2)>>2)
			}
		}
	default: // angular
		angle := intraPredAngle[mode]
		var lineBuf [3*64 + 1]int32
		line := lineBuf[:]
		base := 64 // line[base+k] is ref[k] of the standard, k from -n to 2n
		vertical := mode >= 18
		main := top
		side := left
		if !vertical {
			main, side = left, top
		}
		for k := 0; k <= n; k++ {
			line[base+k] = main(k - 1)
		}
		if angle < 0 {
			if (n*angle)>>5 < -1 {
				inv := invAngle[angle]
				for k := (n * angle) >> 5; k <= -1; k++ {
					line[base+k] = side(-1 + ((k*inv + 128) >> 8))
				}
			}
		} else {
			for k := n + 1; k <= 2*n; k++ {
				line[base+k] = main(k - 1)
			}
		}
		for j := range n { // the line across the direction
			pos := (j + 1) * angle
			idx, fact := pos>>5, int32(pos&31)
			for i := range n {
				var v int32
				if fact != 0 {
					v = ((32-fact)*line[base+i+idx+1] + fact*line[base+i+idx+2] + 16) >> 5
				} else {
					v = line[base+i+idx+1]
				}
				if vertical {
					put(i, j, v)
				} else {
					put(j, i, v)
				}
			}
		}
		if ci == 0 && n < 32 {
			if mode == 26 {
				for y := range n {
					put(0, y, clip(top(0)+((left(y)-left(-1))>>1)))
				}
			} else if mode == 10 {
				for x := range n {
					put(x, 0, clip(left(0)+((top(x)-top(-1))>>1)))
				}
			}
		}
	}
}

func iabs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func iabs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
