package convert

import "context"

// OpenMVCMatroska reads a Matroska file's 3D video, its dependent view in
// a mvcC BlockAddition or a second track, as access units for the MVC
// decoder: next returns each picture's base and dependent view with its
// PTS (90 kHz), and io.EOF at the end; stop closes the file. The other
// tracks are left alone.
func OpenMVCMatroska(ctx context.Context, path string) (next func() (base, dep []byte, pts int64, err error), stop func() error, err error) {
	src, tracks, err := probeMatroska(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	sel, err := SelectTracks(tracks)
	if err != nil {
		return nil, nil, err
	}
	sel.Audio, sel.Subtitles = nil, nil
	g := newMkvDemux(src, sel, "", nil)
	if err := g.start(); err != nil {
		return nil, nil, err
	}
	return g.Next, func() error {
		if g.f == nil {
			return nil
		}
		err := g.f.Close()
		g.f = nil
		return err
	}, nil
}
