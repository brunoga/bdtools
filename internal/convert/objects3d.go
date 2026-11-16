package convert

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/stereo"
)

// Objects graded by hand in --3d-shots: an object painted on a picture in
// --3d-grade and followed through its shot. Its line in the shots file
// gives its number, the stretch it was followed through and its push:
//
//	object 1 0:00:25.5 0:00:31 push=1.5
//
// PUSH is a percent of the picture's width, more bringing the object
// nearer. The object's masks, one for each picture it was followed
// through, are in a file beside the shots file, its name with ".masks"
// added (masksFile).

// object3D is an object of --3d-shots.
type object3D struct {
	ID         int
	Start, End time.Duration
	Push       float64 // percent of the width
}

// masksFile is the masks' file for a shots file.
func masksFile(shots string) string { return shots + ".masks" }

// parseObjectLine reads an object line's fields after "object".
func parseObjectLine(f []string) (object3D, error) {
	var o object3D
	if len(f) < 3 {
		return o, errors.New("an object takes its number, its start and its end")
	}
	id, err := strconv.Atoi(f[0])
	if err != nil || id < 1 || id > 65535 {
		return o, fmt.Errorf("object %q: a number from 1 to 65535", f[0])
	}
	o.ID = id
	if o.Start, err = ParseClock(f[1]); err != nil {
		return o, err
	}
	if o.End, err = ParseClock(f[2]); err != nil {
		return o, err
	}
	if o.End <= o.Start {
		return o, fmt.Errorf("object %d: its end %s is not after its start %s", id, clock(o.End), clock(o.Start))
	}
	for _, kv := range f[3:] {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "push":
			p, err := strconv.ParseFloat(v, 64)
			if err != nil || p < -5 || p > 5 {
				return o, fmt.Errorf("push=%s: -5 to 5 percent of the width", v)
			}
			o.Push = p
		default:
			return o, fmt.Errorf("%q: an object takes push=", kv)
		}
	}
	return o, nil
}

// readObjects3D is the objects of a --3d-shots file.
func readObjects3D(file string) ([]object3D, error) {
	b, err := os.ReadFile(file) //nolint:gosec // the operator's file
	if err != nil {
		return nil, fmt.Errorf("--3d-shots: %w", err)
	}
	var objs []object3D
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "object" {
			continue
		}
		o, err := parseObjectLine(f[1:])
		if err != nil {
			return nil, fmt.Errorf("--3d-shots line %d: %w", n, err)
		}
		objs = append(objs, o)
	}
	return objs, sc.Err()
}

// objectLine is an object as its line in a shots file.
func objectLine(o object3D) string {
	return fmt.Sprintf("object %d %s %s push=%g", o.ID, clock(o.Start), clock(o.End), o.Push)
}

// maskTrack is an object's masks, by the time of the film of the picture
// each is on (in order).
type maskTrack struct {
	at    []time.Duration
	masks [][]byte // stereo.ObjectSide squared bytes each, 0 or 255
}

// near is the mask on the picture at t, within half a picture (tol), nil
// when there is none.
func (m *maskTrack) near(t, tol time.Duration) []byte {
	i := sort.Search(len(m.at), func(i int) bool { return m.at[i] >= t })
	best, bd := -1, tol+1
	for _, j := range []int{i - 1, i} {
		if j >= 0 && j < len(m.at) {
			d := m.at[j] - t
			if d < 0 {
				d = -d
			}
			if d < bd {
				best, bd = j, d
			}
		}
	}
	if best < 0 {
		return nil
	}
	return m.masks[best]
}

// set puts an object's mask on the picture at t (replacing one there).
func (m *maskTrack) set(t time.Duration, mask []byte) {
	i := sort.Search(len(m.at), func(i int) bool { return m.at[i] >= t })
	if i < len(m.at) && m.at[i] == t {
		m.masks[i] = mask
		return
	}
	m.at = append(m.at, 0)
	m.masks = append(m.masks, nil)
	copy(m.at[i+1:], m.at[i:])
	copy(m.masks[i+1:], m.masks[i:])
	m.at[i], m.masks[i] = t, mask
}

// The masks file: masksMagic, then a record a mask: the object's number
// and the picture's time (nanoseconds) as varints, then the mask as runs
// of 0 and 255 in turn (0's first, a run may be empty), its run count and
// the runs as uvarints.
const masksMagic = "bdtools masks 1\n"

// readMasks3D reads a masks file: the objects' masks by number. A file
// that is not there has none.
func readMasks3D(file string) (map[int]*maskTrack, error) {
	out := map[int]*maskTrack{}
	b, err := os.ReadFile(file) //nolint:gosec // beside the operator's shots file
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(b, []byte(masksMagic)) {
		return nil, fmt.Errorf("%s: not a bdtools masks file", file)
	}
	r := bytes.NewReader(b[len(masksMagic):])
	const n = stereo.ObjectSide * stereo.ObjectSide
	for r.Len() > 0 {
		id, err1 := binary.ReadUvarint(r)
		at, err2 := binary.ReadVarint(r)
		runs, err3 := binary.ReadUvarint(r)
		if err := errors.Join(err1, err2, err3); err != nil || runs > n+1 {
			return nil, fmt.Errorf("%s: a damaged record", file)
		}
		mask := make([]byte, n)
		pos := 0
		for k := range runs {
			l, err := binary.ReadUvarint(r)
			if err != nil || pos+int(l) > n {
				return nil, fmt.Errorf("%s: a damaged mask", file)
			}
			if k%2 == 1 {
				for i := pos; i < pos+int(l); i++ {
					mask[i] = 255
				}
			}
			pos += int(l)
		}
		tr := out[int(id)]
		if tr == nil {
			tr = &maskTrack{}
			out[int(id)] = tr
		}
		tr.set(time.Duration(at), mask)
	}
	return out, nil
}

// writeMasks3D writes the objects' masks (through a temporary file beside
// it: a reader never sees half of one).
func writeMasks3D(file string, tracks map[int]*maskTrack) error {
	var b bytes.Buffer
	b.WriteString(masksMagic)
	ids := make([]int, 0, len(tracks))
	for id := range tracks {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	put := func(v uint64) { b.Write(binary.AppendUvarint(nil, v)) }
	for _, id := range ids {
		tr := tracks[id]
		for i, mask := range tr.masks {
			put(uint64(id))
			b.Write(binary.AppendVarint(nil, int64(tr.at[i])))
			var runs []uint64
			on, l := false, uint64(0)
			for _, v := range mask {
				if (v >= 128) != on {
					runs = append(runs, l)
					on, l = !on, 0
				}
				l++
			}
			runs = append(runs, l)
			put(uint64(len(runs)))
			for _, r := range runs {
				put(r)
			}
		}
	}
	tmp := filepath.Join(filepath.Dir(file), "."+filepath.Base(file)+".tmp")
	if err := os.WriteFile(tmp, b.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// objectsAt is the converter's Objects for the objects and their masks, at
// a picture's time of the film (at) within tol: the objects whose stretch
// it is in and that have a mask on it.
func objectsAt(objs []object3D, tracks map[int]*maskTrack, at func(pts int64) time.Duration, tol time.Duration) func(int64) []stereo.ObjectMask {
	return func(pts int64) []stereo.ObjectMask {
		t := at(pts)
		var out []stereo.ObjectMask
		for _, o := range objs {
			if t < o.Start || t >= o.End || o.Push == 0 {
				continue
			}
			if tr := tracks[o.ID]; tr != nil {
				if m := tr.near(t, tol); m != nil {
					out = append(out, stereo.ObjectMask{Mask: m, Push: o.Push / 100})
				}
			}
		}
		return out
	}
}

// objects3DKey is the objects and a digest of their masks, for resuming
// ("" when there are none).
func (o Options) objects3DKey() string {
	if !o.To3D || o.Shots3D == "" {
		return ""
	}
	objs, err := readObjects3D(o.Shots3D)
	if err != nil || len(objs) == 0 {
		return ""
	}
	h := sha256.New()
	for _, ob := range objs {
		fmt.Fprintln(h, objectLine(ob))
	}
	if f, err := os.Open(masksFile(o.Shots3D)); err == nil {
		_, _ = io.Copy(h, f)
		_ = f.Close()
	}
	return hex.EncodeToString(h.Sum(nil))
}
