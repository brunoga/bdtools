package convert

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/stereo"
)

// --3d-shots: stretches of the film graded by hand, for the shots no
// automatic depth gets right. A text file, a line a stretch:
//
//	# START [END] SETTINGS...
//	0:12:30 0:13:05 depth=0.8
//	1:02:10.5 1:02:40 flat
//	1:30:00 screen=-1
//
// START and END are times as a player shows the film (--start's clock);
// without END a stretch runs to the next one's start (or the end). The
// settings are --3d-depth's (depth=), --3d-screen's (screen=) and flat
// (on the screen, as the credits are); what a stretch does not set is the
// command's. Where stretches overlap, the later line's wins.

// shot3D is a stretch of --3d-shots.
type shot3D struct {
	Start, End time.Duration // End 0: open, to the next stretch's start
	Depth      float64       `json:",omitempty"` // 0: the command's
	Screen     *float64      `json:",omitempty"` // nil: the command's
	Flat       bool          `json:",omitempty"`
}

// readShots3D reads a --3d-shots file, its stretches in its lines' order,
// the open ones closed.
func readShots3D(file string) ([]shot3D, error) {
	b, err := os.ReadFile(file) //nolint:gosec // the operator's file
	if err != nil {
		return nil, fmt.Errorf("--3d-shots: %w", err)
	}
	return parseShots3D(string(b))
}

func parseShots3D(text string) ([]shot3D, error) {
	var shots []shot3D
	sc := bufio.NewScanner(strings.NewReader(text))
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		bad := func(format string, a ...any) error {
			return fmt.Errorf("--3d-shots line %d: %s", n, fmt.Sprintf(format, a...))
		}
		var s shot3D
		var err error
		if s.Start, err = ParseClock(f[0]); err != nil {
			return nil, bad("%v", err)
		}
		f = f[1:]
		if len(f) > 0 && !strings.Contains(f[0], "=") && f[0] != "flat" {
			if s.End, err = ParseClock(f[0]); err != nil {
				return nil, bad("%v", err)
			}
			if s.End <= s.Start {
				return nil, bad("its end %s is not after its start %s", clock(s.End), clock(s.Start))
			}
			f = f[1:]
		}
		if len(f) == 0 {
			return nil, bad("no settings (depth=, screen=, flat)")
		}
		for _, kv := range f {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "flat":
				if v != "" {
					return nil, bad("flat takes no value")
				}
				s.Flat = true
			case "depth":
				d, err := strconv.ParseFloat(v, 64)
				if err != nil || d <= 0 || d > 5 {
					return nil, bad("depth=%s: above 0 to 5, as --3d-depth", v)
				}
				s.Depth = d
			case "screen":
				d, err := strconv.ParseFloat(v, 64)
				if err != nil || d < -5 || d > 5 {
					return nil, bad("screen=%s: -5 to 5 percent of the width, as --3d-screen", v)
				}
				s.Screen = &d
			default:
				return nil, bad("%q: want depth=, screen= or flat", kv)
			}
		}
		shots = append(shots, s)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// the open stretches run to the next start after theirs
	for i := range shots {
		if shots[i].End != 0 {
			continue
		}
		for _, o := range shots {
			if o.Start > shots[i].Start && (shots[i].End == 0 || o.Start < shots[i].End) {
				shots[i].End = o.Start
			}
		}
	}
	return shots, nil
}

// shots3DKey is the stretches, for resuming (nil when none, or the file
// cannot be read: Validate has said so).
func (o Options) shots3DKey() []shot3D {
	if !o.To3D || o.Shots3D == "" {
		return nil
	}
	shots, _ := readShots3D(o.Shots3D)
	return shots
}

// shotAt is the stretch a time t of the film is in (the last line's of
// those it is in), -1 for none.
func shotAt(shots []shot3D, t time.Duration) int {
	for i := len(shots) - 1; i >= 0; i-- {
		if s := shots[i]; t >= s.Start && (s.End == 0 || t < s.End) {
			return i
		}
	}
	return -1
}

// shotShapes is the converter's Shot for the stretches, the network's
// size (depth.Model.Size) and the film's clock (pts to a time of it).
func (o Options) shotShapes(shots []shot3D, size string, at func(pts int64) time.Duration) func(int64) (stereo.Shape, bool) {
	base := o.shape3D(size)
	shapes := make([]stereo.Shape, len(shots))
	for i, s := range shots {
		so := o
		if s.Depth > 0 {
			so.Depth3D = s.Depth
		}
		if s.Screen != nil {
			so.Screen3D = *s.Screen
		}
		shapes[i] = so.shape3D(size)
	}
	return func(pts int64) (stereo.Shape, bool) {
		i := shotAt(shots, at(pts))
		if i < 0 {
			return base, false
		}
		return shapes[i], shots[i].Flat
	}
}
