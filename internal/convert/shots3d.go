package convert

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
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
//	1:30:00 screen=-1 ramp=1
//
// START and END are times as a player shows the film (--start's clock);
// without END a stretch runs to the next one's start (or the end). The
// settings are --3d-depth's (depth=), --3d-screen's (screen=) and flat
// (on the screen, as the credits are); what a stretch does not set is the
// command's. Where stretches overlap, the later line's wins. A stretch
// that begins within a shot, not at a cut, takes ramp=SECONDS: its depth
// and screen eased in from the stretch before's over that long (flat is
// not eased).

// shot3D is a stretch of --3d-shots.
type shot3D struct {
	Start, End time.Duration // End 0: open, to the next stretch's start
	Depth      float64       `json:",omitempty"` // 0: the command's
	Screen     *float64      `json:",omitempty"` // nil: the command's
	Flat       bool          `json:",omitempty"`
	Ramp       float64       `json:",omitempty"` // seconds the change eases in over
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
			case "ramp":
				d, err := strconv.ParseFloat(v, 64)
				if err != nil || d < 0 || d > 10 {
					return nil, bad("ramp=%s: seconds, 0 to 10", v)
				}
				s.Ramp = d
			default:
				return nil, bad("%q: want depth=, screen=, flat or ramp=", kv)
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
	shaper := o.shotShaper(shots, size)
	return func(pts int64) (stereo.Shape, bool) { return shaper(at(pts)) }
}

// shotShaper is the shape (and flatness) at a time of the film under the
// stretches: a stretch's own, eased in over its ramp from what was before
// it (the stretch before, or the command's).
func (o Options) shotShaper(shots []shot3D, size string) func(t time.Duration) (stereo.Shape, bool) {
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
	return func(t time.Duration) (stereo.Shape, bool) {
		i := shotAt(shots, t)
		if i < 0 {
			return base, false
		}
		s, sh := shots[i], shapes[i]
		if ramp := time.Duration(s.Ramp * float64(time.Second)); ramp > 0 && t-s.Start < ramp {
			from := base
			if j := shotAt(shots, s.Start-time.Millisecond); j >= 0 && j != i {
				from = shapes[j]
			}
			k := float64(t-s.Start) / float64(ramp)
			sh.Scale = from.Scale + k*(sh.Scale-from.Scale)
			sh.Offset = from.Offset + k*(sh.Offset-from.Offset)
		}
		return sh, s.Flat
	}
}

// writeShots3D writes stretches as a --3d-shots file (through a
// temporary file beside it: a reader never sees half of one).
func writeShots3D(file string, shots []shot3D) error {
	var b strings.Builder
	b.WriteString("# bdtools --3d-shots: START [END] depth=K screen=P [flat] [ramp=S], as --3d-depth and --3d-screen; ramp eases a change made within a shot in over S seconds\n")
	for _, s := range shots {
		b.WriteString(clock(s.Start))
		if s.End > 0 {
			b.WriteString(" " + clock(s.End))
		}
		if s.Depth > 0 {
			fmt.Fprintf(&b, " depth=%g", s.Depth)
		}
		if s.Screen != nil {
			fmt.Fprintf(&b, " screen=%g", *s.Screen)
		}
		if s.Flat {
			b.WriteString(" flat")
		}
		if s.Ramp > 0 {
			fmt.Fprintf(&b, " ramp=%g", s.Ramp)
		}
		if s.Depth <= 0 && s.Screen == nil && !s.Flat {
			b.WriteString(" depth=1")
		}
		b.WriteString("\n")
	}
	tmp := filepath.Join(filepath.Dir(file), "."+filepath.Base(file)+".tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
