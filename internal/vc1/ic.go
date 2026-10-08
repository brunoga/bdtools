package vc1

// Intensity compensation: a P picture can say its reference is to be
// brightened or darkened (a fade) before prediction, by tables made from
// LUMSCALE and LUMSHIFT; each field of a reference has its own.

// initLUT makes the tables for scale and shift; chain applies them over
// what the tables already do (a field compensated twice).
func initLUT(scale, shift int, luty, lutuv *[256]uint8, chain bool) {
	var s, sh int
	if scale == 0 {
		s = -64
		sh = (255 - shift*2) * 64
		if shift > 31 {
			sh += 128 << 6
		}
	} else {
		s = scale + 32
		if shift > 31 {
			sh = (shift - 64) * 64
		} else {
			sh = shift << 6
		}
	}
	for i := range 256 {
		iy, iu := i, i
		if chain {
			iy, iu = int(luty[i]), int(lutuv[i])
		}
		luty[i] = clip8((s*iy + sh + 32) >> 6)
		lutuv[i] = clip8((s*(iu-128) + 128*64 + 32) >> 6)
	}
}

func clip8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func (d *Decoder) currLUTY() *[2][256]uint8 {
	if d.currAux {
		return &d.auxLUTY
	}
	return &d.nextLUTY
}

func (d *Decoder) currLUTUV() *[2][256]uint8 {
	if d.currAux {
		return &d.auxLUTUV
	}
	return &d.nextLUTUV
}

func (d *Decoder) currUseIC() *bool {
	if d.currAux {
		return &d.auxUseIC
	}
	return &d.nextUseIC
}

// rotateLUTs moves the tables on at a picture's start: a reference
// picture's become the last ones, and its own (for the field after it, or
// a B picture's) start as identities.
func (d *Decoder) rotateLUTs() {
	if d.ph.typ == picB || d.ph.typ == picBI {
		d.currAux = true
	} else {
		d.lastUseIC, d.nextUseIC = d.nextUseIC, d.lastUseIC
		d.lastLUTY, d.nextLUTY = d.nextLUTY, d.lastLUTY
		d.lastLUTUV, d.nextLUTUV = d.nextLUTUV, d.lastLUTUV
		d.currAux = false
	}
	y, uv := d.currLUTY(), d.currLUTUV()
	initLUT(32, 0, &y[0], &uv[0], false)
	initLUT(32, 0, &y[1], &uv[1], false)
	*d.currUseIC() = false
}
