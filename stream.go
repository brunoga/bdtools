package mvc

import (
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/brunoga/mvc/m2ts"
)

// Format is the container of a stereo source.
type Format int

const (
	// FormatAnnexB is an Annex B byte stream with both views interleaved
	// (the base view's NAL units, then the dependent view's, per picture).
	FormatAnnexB Format = iota
	// FormatM2TS is a Blu-ray transport stream (PIDs 0x1011 and 0x1012).
	FormatM2TS
	// FormatSplit is a pair of Annex B streams: the base view and, separately,
	// the dependent view, as a demuxer writes them.
	FormatSplit
	// FormatAccessUnits takes paired access units from a function, for a
	// caller doing its own demultiplexing.
	FormatAccessUnits
)

// FormatByName guesses the format from a file name: transport streams by
// extension, everything else Annex B.
func FormatByName(name string) Format {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".m2ts", ".mts", ".ts", ".ssif":
		return FormatM2TS
	}
	return FormatAnnexB
}

// Source is a stereo stream to decode.
type Source struct {
	Format Format
	// R is the stream, or the base view for FormatSplit.
	R io.Reader
	// Dependent is the dependent view for FormatSplit.
	Dependent io.Reader
	// AccessUnits returns the next access unit for FormatAccessUnits: the
	// base view's NAL units, the dependent view's (nil for a 2D picture),
	// both Annex B, and a timestamp carried through to the frame. io.EOF
	// ends the stream.
	AccessUnits func() (base, dep []byte, pts int64, err error)
}

// DecodeOptions control DecodeStream.
type DecodeOptions struct {
	// MaxFrames stops after that many access units (0 decodes everything).
	MaxFrames int
	// Mux, when set, receives the access units as an interleaved Annex B
	// stream (base view then dependent view), which is how the two views of
	// a split source are combined.
	Mux io.Writer
	// OnError receives decoding errors of individual access units, which do
	// not stop the decode; nil ignores them.
	OnError func(error)
}

// Stats counts what a decode produced.
type Stats struct {
	Frames          int // frames output
	DependentFrames int // of which carried a dependent view
	Errors          int // access units reported to OnError
}

// DecodeStream decodes src to the end, calling emit for every frame in
// output order; emit must not retain the frame beyond the call. It returns
// the first error reading the source or returned by emit.
func (d *Decoder) DecodeStream(src Source, opts DecodeOptions, emit func(*StereoFrame) error) (Stats, error) {
	var st Stats
	var emitErr error
	drain := func() {
		for {
			sf, ok := d.NextFrame()
			if !ok {
				return
			}
			st.Frames++
			if sf.Dependent != nil {
				st.DependentFrames++
			}
			if emitErr == nil {
				emitErr = emit(sf)
			}
			sf.Release()
		}
	}
	report := func(err error) {
		if err != nil {
			st.Errors++
			if opts.OnError != nil {
				opts.OnError(err)
			}
		}
	}
	mux := func(b []byte) error {
		if opts.Mux == nil || len(b) == 0 {
			return nil
		}
		_, err := opts.Mux.Write(b)
		return err
	}
	more := func(n int) bool { return (opts.MaxFrames == 0 || n < opts.MaxFrames) && emitErr == nil }
	var readErr error
	switch src.Format {
	case FormatSplit:
		br, dr := NewAUReader(src.R), NewAUReader(src.Dependent)
		for n := 0; more(n); n++ {
			b, err := br.Next()
			if err == io.EOF {
				break
			} else if err != nil {
				readErr = err
				break
			}
			if err := mux(b); err != nil {
				readErr = err
				break
			}
			report(d.DecodeAU(b, int64(n)))
			if dr != nil && !d.opts.BaseOnly {
				if dd, err := dr.Next(); err == nil {
					if err := mux(dd); err != nil {
						readErr = err
						break
					}
					report(d.DecodeAU(dd, int64(n)))
				}
			}
			drain()
		}
	case FormatAccessUnits:
		for n := 0; more(n); n++ {
			b, dd, pts, err := src.AccessUnits()
			if err == io.EOF {
				break
			} else if err != nil {
				readErr = err
				break
			}
			if err := errors.Join(mux(b), mux(dd)); err != nil {
				readErr = err
				break
			}
			report(d.DecodeAU(b, pts))
			if !d.opts.BaseOnly && len(dd) > 0 {
				report(d.DecodeAU(dd, pts))
			}
			drain()
		}
	case FormatM2TS:
		dm := m2ts.NewDemuxer(src.R)
		for n := 0; more(n); n++ {
			au, err := dm.Next()
			if err == io.EOF {
				break
			} else if err != nil {
				readErr = err
				break
			}
			if err := errors.Join(mux(au.Base), mux(au.Dep)); err != nil {
				readErr = err
				break
			}
			report(d.DecodeAU(au.Base, au.PTS))
			if !d.opts.BaseOnly && len(au.Dep) > 0 {
				report(d.DecodeAU(au.Dep, au.PTS))
			}
			dm.Recycle(&au)
			drain()
		}
	default:
		r := NewAUReader(src.R)
		for more(st.Frames) {
			au, err := r.Next()
			if err == io.EOF {
				break
			} else if err != nil {
				readErr = err
				break
			}
			if err := mux(au); err != nil {
				readErr = err
				break
			}
			report(d.DecodeAU(au, -1))
			drain()
		}
	}
	report(d.Flush())
	drain()
	if readErr != nil {
		return st, readErr
	}
	return st, emitErr
}
