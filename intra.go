package mvc

import "encoding/binary"

// Intra prediction (8.3).

// predAngular implements the Intra4x4/Intra8x8 prediction modes for an NxN
// block given top samples top[0..2N-1], left samples left[0..N-1] and the
// top-left sample tl. For Intra8x8 the samples are already filtered.
func predAngular(dst []byte, off, stride, n, mode int, top *[16]int32, left *[8]int32, tl int32,
	haveTop, haveLeft bool) {
	// fast paths for the common modes, writing whole rows
	switch mode {
	case 0: // vertical
		var row [8]byte
		for x := 0; x < n; x++ {
			row[x] = byte(top[x])
		}
		for y := 0; y < n; y++ {
			copy(dst[off+y*stride:off+y*stride+n], row[:n])
		}
		return
	case 1: // horizontal
		for y := 0; y < n; y++ {
			fillRow(dst[off+y*stride:off+y*stride+n], byte(left[y]))
		}
		return
	case 2: // DC
		var sum int32
		var dc int32 = 128
		sh := uint(2)
		if n == 8 {
			sh = 3
		}
		switch {
		case haveTop && haveLeft:
			for i := 0; i < n; i++ {
				sum += top[i] + left[i]
			}
			dc = (sum + int32(n)) >> (sh + 1)
		case haveLeft:
			for i := 0; i < n; i++ {
				sum += left[i]
			}
			dc = (sum + int32(n>>1)) >> sh
		case haveTop:
			for i := 0; i < n; i++ {
				sum += top[i]
			}
			dc = (sum + int32(n>>1)) >> sh
		}
		for y := 0; y < n; y++ {
			fillRow(dst[off+y*stride:off+y*stride+n], byte(dc))
		}
		return
	}
	// P returns p[x,-1] for x >= -1 and p[-1,y] for y >= -1
	pt := func(x int) int32 {
		if x < 0 {
			return tl
		}
		return top[x]
	}
	pl := func(y int) int32 {
		if y < 0 {
			return tl
		}
		return left[y]
	}
	set := func(x, y int, v int32) { dst[off+y*stride+x] = byte(v) }
	switch mode {
	case 0: // vertical
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				set(x, y, top[x])
			}
		}
	case 1: // horizontal
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				set(x, y, left[y])
			}
		}
	case 2: // DC
		var dc int32
		var sum int32
		switch {
		case haveTop && haveLeft:
			for i := 0; i < n; i++ {
				sum += top[i] + left[i]
			}
			if n == 4 {
				dc = (sum + 4) >> 3
			} else {
				dc = (sum + 8) >> 4
			}
		case haveLeft:
			for i := 0; i < n; i++ {
				sum += left[i]
			}
			if n == 4 {
				dc = (sum + 2) >> 2
			} else {
				dc = (sum + 4) >> 3
			}
		case haveTop:
			for i := 0; i < n; i++ {
				sum += top[i]
			}
			if n == 4 {
				dc = (sum + 2) >> 2
			} else {
				dc = (sum + 4) >> 3
			}
		default:
			dc = 128
		}
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				set(x, y, dc)
			}
		}
	case 3: // diagonal down left
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				if x == n-1 && y == n-1 {
					set(x, y, (top[2*n-2]+3*top[2*n-1]+2)>>2)
				} else {
					set(x, y, (top[x+y]+2*top[x+y+1]+top[x+y+2]+2)>>2)
				}
			}
		}
	case 4: // diagonal down right
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				switch {
				case x > y:
					set(x, y, (pt(x-y-2)+2*pt(x-y-1)+pt(x-y)+2)>>2)
				case x < y:
					set(x, y, (pl(y-x-2)+2*pl(y-x-1)+pl(y-x)+2)>>2)
				default:
					set(x, y, (pt(0)+2*tl+pl(0)+2)>>2)
				}
			}
		}
	case 5: // vertical right
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				z := 2*x - y
				switch {
				case z >= 0 && z&1 == 0:
					set(x, y, (pt(x-(y>>1)-1)+pt(x-(y>>1))+1)>>1)
				case z >= 0:
					set(x, y, (pt(x-(y>>1)-2)+2*pt(x-(y>>1)-1)+pt(x-(y>>1))+2)>>2)
				case z == -1:
					set(x, y, (pl(0)+2*tl+pt(0)+2)>>2)
				default:
					set(x, y, (pl(y-2*x-1)+2*pl(y-2*x-2)+pl(y-2*x-3)+2)>>2)
				}
			}
		}
	case 6: // horizontal down
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				z := 2*y - x
				switch {
				case z >= 0 && z&1 == 0:
					set(x, y, (pl(y-(x>>1)-1)+pl(y-(x>>1))+1)>>1)
				case z >= 0:
					set(x, y, (pl(y-(x>>1)-2)+2*pl(y-(x>>1)-1)+pl(y-(x>>1))+2)>>2)
				case z == -1:
					set(x, y, (pl(0)+2*tl+pt(0)+2)>>2)
				default:
					set(x, y, (pt(x-2*y-1)+2*pt(x-2*y-2)+pt(x-2*y-3)+2)>>2)
				}
			}
		}
	case 7: // vertical left
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				i := x + (y >> 1)
				if y&1 == 0 {
					set(x, y, (top[i]+top[i+1]+1)>>1)
				} else {
					set(x, y, (top[i]+2*top[i+1]+top[i+2]+2)>>2)
				}
			}
		}
	case 8: // horizontal up
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				z := x + 2*y
				i := y + (x >> 1)
				switch {
				case z > 2*n-3:
					set(x, y, left[n-1])
				case z == 2*n-3:
					set(x, y, (left[n-2]+3*left[n-1]+2)>>2)
				case z&1 == 0:
					set(x, y, (left[i]+left[i+1]+1)>>1)
				default:
					set(x, y, (left[i]+2*left[i+1]+left[i+2]+2)>>2)
				}
			}
		}
	}
}

// intraAvail describes which neighbouring samples are available.
type intraAvail struct {
	left, top, topRight, topLeft bool
}

// pred4x4 predicts a 4x4 luma block at sample offset off.
func pred4x4Generic(pl []byte, off, stride, mode int, a intraAvail) {
	var top [16]int32
	var left [8]int32
	var tl int32
	if a.top {
		t := pl[off-stride : off-stride+8]
		for i := 0; i < 4; i++ {
			top[i] = int32(t[i])
		}
		if a.topRight {
			for i := 4; i < 8; i++ {
				top[i] = int32(t[i])
			}
		} else {
			for i := 4; i < 8; i++ {
				top[i] = top[3]
			}
		}
	}
	if a.left {
		for i := 0; i < 4; i++ {
			left[i] = int32(pl[off+i*stride-1])
		}
	}
	if a.topLeft {
		tl = int32(pl[off-stride-1])
	}
	predAngular(pl, off, stride, 4, mode, &top, &left, tl, a.top, a.left)
}

// pred8x8L predicts an 8x8 luma block with reference sample filtering.
func pred8x8LGeneric(pl []byte, off, stride, mode int, a intraAvail) {
	var p [16]int32 // unfiltered top incl. top-right
	var l [8]int32
	var ptl int32
	if a.top {
		t := pl[off-stride : off-stride+16]
		for i := 0; i < 8; i++ {
			p[i] = int32(t[i])
		}
		if a.topRight {
			for i := 8; i < 16; i++ {
				p[i] = int32(t[i])
			}
		} else {
			for i := 8; i < 16; i++ {
				p[i] = p[7]
			}
		}
	}
	if a.left {
		for i := 0; i < 8; i++ {
			l[i] = int32(pl[off+i*stride-1])
		}
	}
	if a.topLeft {
		ptl = int32(pl[off-stride-1])
	}
	var top [16]int32
	var left [8]int32
	var tl int32
	if a.top {
		if a.topLeft {
			top[0] = (ptl + 2*p[0] + p[1] + 2) >> 2
		} else {
			top[0] = (3*p[0] + p[1] + 2) >> 2
		}
		for x := 1; x < 15; x++ {
			top[x] = (p[x-1] + 2*p[x] + p[x+1] + 2) >> 2
		}
		top[15] = (p[14] + 3*p[15] + 2) >> 2
	}
	if a.topLeft {
		switch {
		case a.top && a.left:
			tl = (p[0] + 2*ptl + l[0] + 2) >> 2
		case a.top:
			tl = (3*ptl + p[0] + 2) >> 2
		case a.left:
			tl = (3*ptl + l[0] + 2) >> 2
		default:
			tl = ptl
		}
	}
	if a.left {
		if a.topLeft {
			left[0] = (ptl + 2*l[0] + l[1] + 2) >> 2
		} else {
			left[0] = (3*l[0] + l[1] + 2) >> 2
		}
		for y := 1; y < 7; y++ {
			left[y] = (l[y-1] + 2*l[y] + l[y+1] + 2) >> 2
		}
		left[7] = (l[6] + 3*l[7] + 2) >> 2
	}
	predAngular(pl, off, stride, 8, mode, &top, &left, tl, a.top, a.left)
}

// pred16x16 predicts a 16x16 luma macroblock.
func pred16x16(pl []byte, off, stride, mode int, haveTop, haveLeft, haveTL bool) {
	switch mode {
	case 0:
		t := pl[off-stride : off-stride+16]
		for y := 0; y < 16; y++ {
			copy(pl[off+y*stride:off+y*stride+16], t)
		}
	case 1:
		for y := 0; y < 16; y++ {
			fillRow(pl[off+y*stride:off+y*stride+16], pl[off+y*stride-1])
		}
	case 2:
		var sum int32
		var dc int32
		switch {
		case haveTop && haveLeft:
			for i := 0; i < 16; i++ {
				sum += int32(pl[off-stride+i]) + int32(pl[off+i*stride-1])
			}
			dc = (sum + 16) >> 5
		case haveLeft:
			for i := 0; i < 16; i++ {
				sum += int32(pl[off+i*stride-1])
			}
			dc = (sum + 8) >> 4
		case haveTop:
			for i := 0; i < 16; i++ {
				sum += int32(pl[off-stride+i])
			}
			dc = (sum + 8) >> 4
		default:
			dc = 128
		}
		for y := 0; y < 16; y++ {
			fillRow(pl[off+y*stride:off+y*stride+16], byte(dc))
		}
	case 3:
		predPlane(pl, off, stride, 16, 16)
	}
}

// predPlane implements plane prediction for a w x h block (16x16 luma or
// 8x8 chroma).
func predPlaneGeneric(pl []byte, off, stride, w, h int) {
	top := func(x int) int32 { return int32(pl[off-stride+x]) }
	left := func(y int) int32 { return int32(pl[off+y*stride-1]) }
	xc, yc := w/2-1, h/2-1 // 7 or 3
	var H, V int32
	for x := 0; x <= xc; x++ {
		H += int32(x+1) * (top(xc+1+x) - top(xc-1-x))
	}
	for y := 0; y <= yc; y++ {
		V += int32(y+1) * (left(yc+1+y) - left(yc-1-y))
	}
	a := 16 * (left(h-1) + top(w-1))
	var b, c int32
	if w == 16 {
		b = (5*H + 32) >> 6
	} else {
		b = (34*H + 32) >> 6
	}
	if h == 16 {
		c = (5*V + 32) >> 6
	} else {
		c = (34*V + 32) >> 6
	}
	for y := 0; y < h; y++ {
		r := pl[off+y*stride : off+y*stride+w]
		base := a + c*int32(y-yc) + 16 - b*int32(xc)
		for x := range r {
			r[x] = clip255((base + b*int32(x)) >> 5)
		}
	}
}

// predChroma predicts an 8x8 chroma block (4:2:0).
func predChromaGeneric(pl []byte, off, stride, mode int, haveTop, haveLeft bool) {
	switch mode {
	case 0: // DC per 4x4 block
		for by := 0; by < 2; by++ {
			for bx := 0; bx < 2; bx++ {
				o := off + by*4*stride + bx*4
				var st, sl int32
				if haveTop {
					for i := 0; i < 4; i++ {
						st += int32(pl[off-stride+bx*4+i])
					}
				}
				if haveLeft {
					for i := 0; i < 4; i++ {
						sl += int32(pl[off+(by*4+i)*stride-1])
					}
				}
				var dc int32 = 128
				if bx == by { // (0,0) and (4,4)
					switch {
					case haveTop && haveLeft:
						dc = (st + sl + 4) >> 3
					case haveLeft:
						dc = (sl + 2) >> 2
					case haveTop:
						dc = (st + 2) >> 2
					}
				} else if bx == 1 { // (4,0)
					switch {
					case haveTop:
						dc = (st + 2) >> 2
					case haveLeft:
						dc = (sl + 2) >> 2
					}
				} else { // (0,4)
					switch {
					case haveLeft:
						dc = (sl + 2) >> 2
					case haveTop:
						dc = (st + 2) >> 2
					}
				}
				for y := 0; y < 4; y++ {
					r := pl[o+y*stride : o+y*stride+4]
					r[0], r[1], r[2], r[3] = byte(dc), byte(dc), byte(dc), byte(dc)
				}
			}
		}
	case 1: // horizontal
		for y := 0; y < 8; y++ {
			fillRow(pl[off+y*stride:off+y*stride+8], pl[off+y*stride-1])
		}
	case 2: // vertical
		t := pl[off-stride : off-stride+8]
		for y := 0; y < 8; y++ {
			copy(pl[off+y*stride:off+y*stride+8], t)
		}
	case 3:
		predPlane(pl, off, stride, 8, 8)
	}
}

// fillRow sets a row of 4, 8 or 16 bytes to v.
func fillRow(r []byte, v byte) {
	w := uint64(v) * 0x0101010101010101
	switch len(r) {
	case 4:
		binary.LittleEndian.PutUint32(r, uint32(w))
	case 8:
		binary.LittleEndian.PutUint64(r, w)
	case 16:
		binary.LittleEndian.PutUint64(r, w)
		binary.LittleEndian.PutUint64(r[8:], w)
	default:
		for i := range r {
			r[i] = v
		}
	}
}
