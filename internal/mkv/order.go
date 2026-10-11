package mkv

import "errors"

// HEVCOrder puts an HEVC stream's access units, met in decoding order, in
// display order: Key gives each one a number that sorts it there, its
// picture order count past every earlier coded video sequence's. Decoders
// that give pictures back in the order of the timestamps they were given
// (VideoToolbox) need such timestamps.
type HEVCOrder struct {
	params paramSets
	poc    hevcPOC
	base   int64 // the coded video sequence's POC 0
	next   int64 // past every key given
}

// cvsGap is room between coded video sequences' keys for the leading
// pictures of the next, whose POCs are negative.
const cvsGap = 1 << 20

// Key is an Annex B access unit's display key.
func (o *HEVCOrder) Key(au []byte) (int64, error) {
	nals, _ := splitNALsAt(au)
	for _, n := range nals {
		if len(n) < 3 {
			continue
		}
		switch t := n[0] >> 1 & 0x3f; {
		case t == 32:
			o.params.addHEVC(0, n)
		case t == 33:
			o.params.addHEVCSPS(n)
		case t == 34:
			o.params.addHEVCPPS(n)
		case t < 32 && n[2]&0x80 != 0: // the picture's first slice segment
			poc, _, reset, err := o.poc.poc(n, &o.params)
			if err != nil {
				return 0, err
			}
			if reset {
				o.base = o.next + cvsGap
			}
			k := o.base + poc
			o.next = max(o.next, k+1)
			return k, nil
		}
	}
	return 0, errors.New("mkv: an access unit without a picture")
}
