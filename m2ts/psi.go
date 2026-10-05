package m2ts

// Program-specific information: the PAT, to find the PMT, and the PMT, to
// know the streams. Blu-ray carries one program; the first is taken.

// crcMPEG is the MSB-first table of the CRC-32/MPEG-2 polynomial; the
// standard library's tables are bit-reflected and do not apply.
var crcMPEG = func() (t [256]uint32) {
	for i := range t {
		c := uint32(i) << 24
		for k := 0; k < 8; k++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return
}()

// section assembles a PSI section on a PID and parses it when complete.
func (r *Reader) section(pid uint16, pusi bool, payload []byte) {
	if pusi {
		if len(payload) < 1 {
			return
		}
		ptr := int(payload[0])
		if 1+ptr > len(payload) {
			return
		}
		r.sections[pid] = append([]byte(nil), payload[1+ptr:]...)
	} else if buf, ok := r.sections[pid]; ok {
		r.sections[pid] = append(buf, payload...)
	} else {
		return
	}
	buf := r.sections[pid]
	if len(buf) < 3 {
		return
	}
	length := int(buf[1]&0x0f)<<8 | int(buf[2])
	if len(buf) < 3+length {
		return // more packets to come
	}
	sec := buf[:3+length]
	delete(r.sections, pid)
	if length < 4 || mpegCRC(sec) != 0 {
		return // damaged
	}
	body := sec[8 : len(sec)-4] // after table_id..last_section_number
	switch sec[0] {
	case 0x00: // PAT
		if pid != 0 {
			return
		}
		for i := 0; i+4 <= len(body); i += 4 {
			num := uint16(body[i])<<8 | uint16(body[i+1])
			mapPID := uint16(body[i+2]&0x1f)<<8 | uint16(body[i+3])
			if num != 0 {
				r.pmtPIDs[mapPID] = true
			}
		}
	case 0x02: // PMT
		if len(body) < 4 {
			return
		}
		// An SSIF interleaves two clips, each with its own tables: the
		// dependent view's PMT lists only its PID. The streams of every PMT
		// seen are merged, so the program describes the whole file.
		infoLen := int(body[2]&0x0f)<<8 | int(body[3])
		if 4+infoLen > len(body) {
			return
		}
		if r.program == nil {
			r.program = &Program{Number: uint16(sec[3])<<8 | uint16(sec[4]), Version: sec[5] >> 1 & 0x1f}
			r.program.PCRPID = uint16(body[0]&0x1f)<<8 | uint16(body[1])
			r.program.Info = append([]byte(nil), body[4:4+infoLen]...)
		}
		prog := r.program
		i := 4 + infoLen
		for i+5 <= len(body) {
			s := ProgramStream{Type: body[i], PID: uint16(body[i+1]&0x1f)<<8 | uint16(body[i+2])}
			esLen := int(body[i+3]&0x0f)<<8 | int(body[i+4])
			i += 5
			if i+esLen > len(body) {
				break
			}
			s.Lang = language(body[i : i+esLen])
			s.Descriptors = append([]byte(nil), body[i:i+esLen]...)
			i += esLen
			if _, have := prog.Stream(s.PID); !have {
				prog.Streams = append(prog.Streams, s)
			}
		}
	}
}

// language returns the ISO 639 code from an ES descriptor loop, if present.
func language(desc []byte) string {
	for i := 0; i+2 <= len(desc); {
		tag, l := desc[i], int(desc[i+1])
		i += 2
		if i+l > len(desc) {
			break
		}
		if tag == 0x0a && l >= 3 {
			return string(desc[i : i+3])
		}
		i += l
	}
	return ""
}

// mpegCRC computes the CRC-32/MPEG-2 of a section; it is 0 over a section
// that includes its own CRC.
func mpegCRC(b []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, c := range b {
		crc = crc<<8 ^ crcMPEG[byte(crc>>24)^c]
	}
	return crc
}

// Section builds a PSI section: the header, the body, and the CRC.
func Section(tableID byte, ext uint16, version byte, body []byte) []byte {
	n := 5 + len(body) + 4
	sec := make([]byte, 0, 3+n)
	sec = append(sec, tableID, 0xb0|byte(n>>8&0x0f), byte(n))
	sec = append(sec, byte(ext>>8), byte(ext), 0xc1|version<<1, 0, 0)
	sec = append(sec, body...)
	crc := mpegCRC(sec)
	return append(sec, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

// PATBody is the body of a PAT listing one program.
func PATBody(program, pmtPID uint16) []byte {
	return []byte{byte(program >> 8), byte(program), 0xe0 | byte(pmtPID>>8), byte(pmtPID)}
}

// PMTBody is the body of a PMT for prog's streams.
func PMTBody(prog *Program) []byte {
	b := []byte{0xe0 | byte(prog.PCRPID>>8), byte(prog.PCRPID), 0xf0 | byte(len(prog.Info)>>8), byte(len(prog.Info))}
	b = append(b, prog.Info...)
	for _, s := range prog.Streams {
		b = append(b, s.Type, 0xe0|byte(s.PID>>8), byte(s.PID), 0xf0|byte(len(s.Descriptors)>>8), byte(len(s.Descriptors)))
		b = append(b, s.Descriptors...)
	}
	return b
}
