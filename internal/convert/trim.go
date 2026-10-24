package convert

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Trimming (--start, --end, --duration): the part of the title or file
// converted, on its own timeline (a disc title's as the playlist plays
// it, a file's from its first block). The demuxers window what they give
// to it, as a playlist's IN and OUT times window a disc's clips: the
// picture decoded from the keyframe before and kept from the start, the
// sound and subtitles cut at the start and the end, everything moved to
// begin at zero.

// Trim is the part of the source converted; zero is all of it.
type Trim struct {
	Start, End time.Duration // End 0: to the end
}

// span is how long the part converted plays of a source length long (0
// when that is not known).
func (t Trim) span(length time.Duration) time.Duration {
	if length <= 0 {
		return 0
	}
	end := length
	if t.End > 0 {
		end = min(t.End, length)
	}
	return max(end-t.Start, 0)
}

// IsZero reports whether everything is converted.
func (t Trim) IsZero() bool { return t == Trim{} }

func (t Trim) String() string {
	if t.End == 0 {
		return fmt.Sprintf("from %s", clock(t.Start))
	}
	return fmt.Sprintf("%s to %s", clock(t.Start), clock(t.End))
}

// clock is a time as H:MM:SS.mmm.
func clock(d time.Duration) string {
	h, m := d/time.Hour, d%time.Hour/time.Minute
	s := float64(d%time.Minute) / float64(time.Second)
	return fmt.Sprintf("%d:%02d:%06.3f", h, m, s)
}

// ParseClock reads a time: [[H:]M:]S[.fraction], or a Go duration (90s,
// 1h30m).
func ParseClock(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if d, err := time.ParseDuration(s); err == nil && strings.ContainsAny(s, "hms") {
		return d, nil
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("%q: want [[H:]M:]S, or a duration (90s, 1h30m)", s)
	}
	var d time.Duration
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 || math.IsInf(v, 0) || (i > 0 && v >= 60) {
			return 0, fmt.Errorf("%q: want [[H:]M:]S, or a duration (90s, 1h30m)", s)
		}
		unit := time.Second
		switch len(parts) - 1 - i {
		case 1:
			unit = time.Minute
		case 2:
			unit = time.Hour
		}
		d += time.Duration(v * float64(unit))
	}
	return d, nil
}

// trimWindows narrows a title's clip windows (90 kHz IN and OUT times,
// each clip's start on the title's timeline at offsets) to the trim: a
// clip outside it gets an empty window. It gives the new offsets.
func trimWindows(ins, outs []int64, t Trim) []int64 {
	start := int64(t.Start) * 90000 / int64(time.Second)
	end := int64(math.MaxInt64)
	if t.End > 0 {
		end = int64(t.End) * 90000 / int64(time.Second)
	}
	offsets := make([]int64, len(ins))
	var at, acc int64 // the title's time at the clip's start, before and after the cut
	for k := range ins {
		in, out := ins[k], outs[k]
		length := out - in
		lo, hi := max(start-at, 0), min(end-at, length)
		offsets[k] = acc
		if hi <= lo {
			ins[k], outs[k] = in, in // nothing of it
		} else {
			ins[k], outs[k] = in+lo, in+hi
			acc += hi - lo
		}
		at += length
	}
	return offsets
}

// trimChapters moves the chapters onto the trimmed timeline: those before
// the start go, but the last of them, which the start falls in, starts
// at zero.
func trimChapters(ch []time.Duration, t Trim) []time.Duration {
	if t.IsZero() {
		return ch
	}
	var out []time.Duration
	for i, c := range ch {
		next := time.Duration(math.MaxInt64)
		if i+1 < len(ch) {
			next = ch[i+1]
		}
		if t.End > 0 && c >= t.End {
			break
		}
		if next <= t.Start {
			continue
		}
		out = append(out, max(c-t.Start, 0))
	}
	return out
}
