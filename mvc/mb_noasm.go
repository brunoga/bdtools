//go:build !amd64 || purego

package mvc

func (s *sliceDec) clearMBGrids(base, st int) {
	pic := s.pic
	for j := 0; j < 4; j++ {
		o := base + j*st
		for l := 0; l < 2; l++ {
			r := pic.refs[l][o : o+4]
			r[0], r[1], r[2], r[3] = -1, -1, -1, -1
			m := pic.mvs[l][o : o+4]
			m[0], m[1], m[2], m[3] = mv{}, mv{}, mv{}, mv{}
			if s.cabacOn {
				d := s.fc.mvd[l][o : o+4]
				d[0], d[1], d[2], d[3] = [2]uint8{}, [2]uint8{}, [2]uint8{}, [2]uint8{}
			}
		}
		i4 := s.fc.i4[o : o+4]
		i4[0], i4[1], i4[2], i4[3] = 2, 2, 2, 2
	}
}

func (s *sliceDec) fillMotionMB(l int, ref int8, m mv) { s.fillMotionGo(l, 0, 0, 4, 4, ref, m) }

func (s *sliceDec) fillIDs(base int, id0, id1 int32) {
	st := s.fc.mbW * 4
	for j := 0; j < 4; j++ {
		o := s.fc.refIDs[0][base+j*st : base+j*st+4]
		o[0], o[1], o[2], o[3] = id0, id0, id0, id0
		o = s.fc.refIDs[1][base+j*st : base+j*st+4]
		o[0], o[1], o[2], o[3] = id1, id1, id1, id1
	}
}
