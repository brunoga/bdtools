package mvc

import (
	"bufio"
	"io"
)

// AUReader splits an Annex B byte stream into access units. Use two
// readers to pair a base view stream with a separately stored dependent
// view stream (e.g. the .264 and .mvc files written by tsMuxeR).
type AUReader struct {
	r      *bufio.Reader
	buf    []byte // unconsumed input
	au     []byte // access unit being assembled
	hasVCL bool
	eof    bool
}

// NewAUReader returns an AUReader reading from r.
func NewAUReader(r io.Reader) *AUReader {
	return &AUReader{r: bufio.NewReaderSize(r, 1<<20)}
}

// nextNAL returns the next NAL unit including its start code.
func (a *AUReader) nextNAL() ([]byte, error) {
	for {
		start := findStartCode(a.buf, 0)
		if start >= 0 {
			if next := findStartCode(a.buf, start+3); next >= 0 {
				// include a leading zero byte of a 4-byte start code in the next NAL
				end := next
				if end > start+3 && a.buf[end-1] == 0 {
					end--
				}
				nal := a.buf[:end] // keep leading zero bytes
				a.buf = a.buf[end:]
				return nal, nil
			}
		}
		if a.eof {
			if start >= 0 && len(a.buf) > start+3 {
				nal := a.buf
				a.buf = a.buf[len(a.buf):]
				return nal, nil
			}
			return nil, io.EOF
		}
		// read more data into a fresh buffer holding the unconsumed tail
		nb := make([]byte, len(a.buf), len(a.buf)+1<<20)
		copy(nb, a.buf)
		n, err := io.ReadFull(a.r, nb[len(nb):cap(nb)])
		a.buf = nb[:len(nb)+n]
		if err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
		if err == io.EOF {
			a.eof = true
		} else if err != nil {
			return nil, err
		}
	}
}

// startsAU reports whether the NAL unit begins a new access unit given
// that the current one already contains a VCL NAL unit (7.4.1.2.3).
func startsAU(nal []byte) bool {
	i := nalHeaderPos(nal)
	if i < 0 || i >= len(nal) {
		return false
	}
	typ := nal[i] & 31
	switch typ {
	case nalAUD, nalSPS, nalPPS, nalSEI, nalPrefix, nalSubsetSPS, nalDepDelimiter, 16, 17, 18:
		return true
	case nalSlice, nalSliceIDR, nalSliceExt:
		hl := 1
		if typ == nalSliceExt {
			hl = 4
		}
		if i+hl >= len(nal) {
			return false
		}
		var rb [16]byte
		raw := nal[i+hl:]
		if len(raw) > 16 {
			raw = raw[:16]
		}
		var br bitReader
		br.init(unescapeRBSP(rb[:0], raw))
		return br.ue() == 0 // first_mb_in_slice
	}
	return false
}

// nalHeaderPos returns the index of the NAL header byte after the start
// code, or -1.
func nalHeaderPos(nal []byte) int {
	sc := findStartCode(nal, 0)
	if sc < 0 {
		return -1
	}
	return sc + 3
}

func isVCL(nal []byte) bool {
	i := nalHeaderPos(nal)
	if i < 0 || i >= len(nal) {
		return false
	}
	t := nal[i] & 31
	return t == nalSlice || t == nalSliceIDR || t == nalSliceExt
}

// Next returns the next access unit (Annex B, with start codes).
func (a *AUReader) Next() ([]byte, error) {
	for {
		nal, err := a.nextNAL()
		if err == io.EOF {
			if len(a.au) > 0 {
				out := a.au
				a.au = nil
				a.hasVCL = false
				return out, nil
			}
			return nil, io.EOF
		} else if err != nil {
			return nil, err
		}
		if a.hasVCL && startsAU(nal) {
			out := a.au
			a.au = append(make([]byte, 0, max(len(out), 4096)), nal...)
			a.hasVCL = isVCL(nal)
			return out, nil
		}
		a.au = append(a.au, nal...)
		if isVCL(nal) {
			a.hasVCL = true
		}
	}
}
