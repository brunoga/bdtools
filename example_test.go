package mvc_test

import (
	"fmt"
	"io"
	"os"

	"github.com/brunoga/mvc"
	"github.com/brunoga/mvc/m2ts"
)

// Decode a 3D Blu-ray transport stream into pairs of full frames.
func Example() {
	f, err := os.Open("movie.m2ts")
	if err != nil {
		return
	}
	defer f.Close()

	dec := mvc.NewDecoder(mvc.Options{})
	dm := m2ts.NewDemuxer(f)
	consume := func(sf *mvc.StereoFrame) {
		left, right := sf.Base, sf.Dependent // right is nil for 2D access units
		fmt.Println(left.Width, left.Height, left.PTS, right != nil)
		sf.Release() // return the buffers to the decoder
	}
	for {
		au, err := dm.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return
		}
		dec.DecodeAU(au.Base, au.PTS)
		dec.DecodeAU(au.Dep, au.PTS)
		dm.Recycle(&au)
		for {
			sf, ok := dec.NextFrame()
			if !ok {
				break
			}
			consume(sf)
		}
	}
	dec.Flush()
	for {
		sf, ok := dec.NextFrame()
		if !ok {
			break
		}
		consume(sf)
	}
}
