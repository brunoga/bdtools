package convert

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/m2ts"
)

// Decoding with ffmpeg: the fallback where no GPU decoder works.
//
// The access units go to ffmpeg as an MPEG transport stream on its standard
// input, each in a PES with its timestamp; the pictures come back raw on
// its standard output, in display order, and each picture's time on a
// third pipe (-stats_enc_pre, ffmpeg 6.1 and later). So every picture
// carries its own access unit's timestamp, whatever the decoder drops (the
// leading pictures of a stream that starts on a CRA) or reorders. It is a
// gpu.Decoder, so everything built on the GPU decoders (HDR metadata,
// Dolby Vision's layers) works over it.

// ffDecoder decodes through an ffmpeg process.
type ffDecoder struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	picture func(*gpu.DecodedPicture) error

	// The transport stream written to ffmpeg, and the timestamps it was
	// given: the decoder's, which may not fit 33 bits, are moved onto a
	// timeline of their own, clip after clip.
	streamType byte
	cc         [2]byte
	fedPTS     map[int64]int64 // ffmpeg's timestamp -> the caller's
	clipFirst  map[int64]int64
	clipStart  map[int64]int64
	fedMax     int64
	wrote      bool

	writes chan []byte     // to the writing goroutine
	frames chan *ffPicture // from the reading goroutine
	free   chan *ffPicture
	done   chan struct{} // closed when reading has stopped
	stderr *ffLog
	err    error
	last   int64 // the latest picture's timestamp, as the caller gave it
	eof    bool
}

// ffPicture is a decoded picture, in its own buffer.
type ffPicture struct {
	pic gpu.DecodedPicture
	buf []byte
	err error // the reading's end (io.EOF) or failure, without a picture
}

// ffBuffers is how many pictures can wait between ffmpeg and the caller.
const ffBuffers = 4

var ffCodecs = map[gpu.VideoCodec]byte{
	gpu.DecodeH264:  m2ts.TypeAVC,
	gpu.DecodeHEVC:  m2ts.TypeHEVC,
	gpu.DecodeVC1:   m2ts.TypeVC1,
	gpu.DecodeMPEG2: m2ts.TypeMPEG2Video,
}

// openFFDecoder starts ffmpeg (bin) decoding a stream of cfg's codec.
func openFFDecoder(bin string, cfg gpu.DecodeConfig, picture func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
	st, ok := ffCodecs[cfg.Codec]
	if !ok {
		return nil, fmt.Errorf("ffmpeg decoding: codec %s", cfg.Codec)
	}
	d := &ffDecoder{picture: picture, streamType: st, last: math.MinInt64, fedPTS: map[int64]int64{}, clipFirst: map[int64]int64{},
		clipStart: map[int64]int64{}, writes: make(chan []byte, 8), frames: make(chan *ffPicture, ffBuffers),
		free: make(chan *ffPicture, ffBuffers+2), done: make(chan struct{}), stderr: newFFLog()}
	statsR, statsW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	// Interlaced pictures (VC-1 and MPEG-2 discs are often 1080i) are
	// deinterlaced to one frame each, as NVDEC does; progressive ones pass.
	threads := "0" // ffmpeg's choice: as many frame threads, and pictures held back, as processors
	if cfg.LowDelay {
		threads = "4"
	}
	// Close ends it: the decoder's life is its caller's, not a context's.
	d.cmd = exec.CommandContext(context.Background(), bin, "-hide_banner", "-nostats", "-loglevel", "info", //nolint:gosec // the ffmpeg we resolved
		"-threads", threads, "-f", "mpegts", "-i", "pipe:0", "-map", "0:v:0", "-copyts", "-fps_mode", "passthrough",
		"-vf", "bwdif=mode=send_frame:deint=interlaced", "-enc_time_base", "1/90000", "-f", "rawvideo",
		"-stats_enc_pre", "pipe:3", "-stats_enc_pre_fmt", "{pts} {tb}", "pipe:1")
	d.cmd.ExtraFiles = []*os.File{statsW}
	d.cmd.Stderr = d.stderr
	if d.stdin, err = d.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	stdout, err := d.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := d.cmd.Start(); err != nil {
		_, _ = statsR.Close(), statsW.Close()
		return nil, fmt.Errorf("%w: starting ffmpeg: %w", gpu.ErrDecodeUnavailable, err)
	}
	_ = statsW.Close()
	go d.write()
	go d.read(bufio.NewReaderSize(stdout, 4<<20), statsR)
	return d, nil
}

// write feeds ffmpeg; a failure ends with stdin closed, which ends ffmpeg.
func (d *ffDecoder) write() {
	for b := range d.writes {
		if _, err := d.stdin.Write(b); err != nil {
			for range d.writes { //nolint:revive // drain: ffmpeg has gone, its error says why
			}
			break
		}
	}
	_ = d.stdin.Close()
}

// read takes the pictures ffmpeg writes, in display order, with their
// times.
func (d *ffDecoder) read(out *bufio.Reader, stats *os.File) {
	defer close(d.done)
	defer func() { _ = stats.Close() }()
	times := bufio.NewScanner(stats)
	fail := func(err error) { d.frames <- &ffPicture{err: err} }
	// ffmpeg states the output's format before its first picture; if it
	// ends without one, it has said why.
	if _, err := out.Peek(1); err != nil {
		d.stderr.end()
	}
	f, err := d.stderr.format()
	if err != nil {
		fail(err)
		return
	}
	bps := 1
	if f.depth > 8 {
		bps = 2
	}
	planar := f.w * f.h * bps * 3 / 2
	raw := make([]byte, planar)
	for {
		if _, err := io.ReadFull(out, raw); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.EOF
			} else if errors.Is(err, io.ErrUnexpectedEOF) {
				err = fmt.Errorf("ffmpeg's output ends inside a picture")
			}
			fail(err)
			return
		}
		if !times.Scan() {
			fail(errors.New("ffmpeg gave a picture without its time"))
			return
		}
		pts, err := ffTime(times.Text())
		if err != nil {
			fail(err)
			return
		}
		var p *ffPicture
		select {
		case p = <-d.free:
		default:
			p = &ffPicture{}
		}
		if len(p.buf) != planar {
			p.buf = make([]byte, planar)
		}
		toSemiPlanar(p.buf, raw, f.w, f.h, bps)
		row := f.w * bps
		p.pic = gpu.DecodedPicture{Width: f.w, Height: f.h, Depth: f.depth, Y: p.buf[:row*f.h], UV: p.buf[row*f.h:],
			Pitch: row, PTS: pts, Color: f.color, FrameRateNum: f.num, FrameRateDen: f.den}
		d.frames <- p
	}
}

// ffTime reads a picture's time as -stats_enc_pre gives it ("pts num/den")
// in 90 kHz units.
func ffTime(line string) (int64, error) {
	f := strings.Fields(line)
	if len(f) == 2 {
		n, d, ok := strings.Cut(f[1], "/")
		pts, err1 := strconv.ParseInt(f[0], 10, 64)
		num, err2 := strconv.ParseInt(n, 10, 64)
		den, err3 := strconv.ParseInt(d, 10, 64)
		if ok && err1 == nil && err2 == nil && err3 == nil && den > 0 {
			return int64(math.Round(float64(pts) * float64(num) * 90000 / float64(den))), nil
		}
	}
	return 0, fmt.Errorf("ffmpeg's picture time %q", line)
}

// toSemiPlanar turns ffmpeg's planar 4:2:0 (low-aligned samples above 8
// bits) into NV12 or P010: luma, then Cb and Cr interleaved, 10 bits in a
// sample's top bits.
func toSemiPlanar(dst, src []byte, w, h, bps int) {
	ys, cs := w*h*bps, w/2*(h/2)*bps
	y, cb, cr := src[:ys], src[ys:ys+cs], src[ys+cs:ys+2*cs]
	if bps == 1 {
		copy(dst, y)
		uv := dst[ys:]
		for i := range cs {
			uv[2*i], uv[2*i+1] = cb[i], cr[i]
		}
		return
	}
	for i := 0; i < ys; i += 2 {
		v := (uint16(y[i]) | uint16(y[i+1])<<8) << 6
		dst[i], dst[i+1] = byte(v), byte(v>>8)
	}
	uv := dst[ys:]
	for i := 0; i < cs; i += 2 {
		b := (uint16(cb[i]) | uint16(cb[i+1])<<8) << 6
		r := (uint16(cr[i]) | uint16(cr[i+1])<<8) << 6
		uv[2*i], uv[2*i+1], uv[2*i+2], uv[2*i+3] = byte(b), byte(b>>8), byte(r), byte(r>>8)
	}
}

// Decode sends an access unit to ffmpeg, handing on the pictures that have
// come back meanwhile.
func (d *ffDecoder) Decode(au []byte, pts int64) error {
	if d.err != nil {
		return d.err
	}
	ts := d.packetize(au, pts)
	for {
		select {
		case d.writes <- ts:
			return d.deliver(false)
		case p := <-d.frames:
			if err := d.hand(p); err != nil {
				return err
			}
		}
	}
}

// Flush ends the stream and hands on every picture still to come.
func (d *ffDecoder) Flush() error {
	if d.err != nil {
		return d.err
	}
	if d.writes != nil {
		close(d.writes)
		d.writes = nil
	}
	if err := d.deliver(true); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if err := d.cmd.Wait(); err != nil {
		d.err = fmt.Errorf("ffmpeg: %w%s", err, d.stderr.tail())
		return d.err
	}
	return nil
}

// deliver hands on the pictures that are there (all, until the end, when
// wait is set).
func (d *ffDecoder) deliver(wait bool) error {
	for {
		var p *ffPicture
		if wait {
			p = <-d.frames
		} else {
			select {
			case p = <-d.frames:
			default:
				return nil
			}
		}
		if err := d.hand(p); err != nil {
			return err
		}
	}
}

func (d *ffDecoder) hand(p *ffPicture) error {
	if p.err != nil {
		if errors.Is(p.err, io.EOF) {
			d.eof = true
			return io.EOF
		}
		d.err = fmt.Errorf("ffmpeg: %w%s", p.err, d.stderr.tail())
		return d.err
	}
	if orig, ok := d.fedPTS[p.pic.PTS]; ok {
		delete(d.fedPTS, p.pic.PTS)
		p.pic.PTS = orig
	}
	d.last = p.pic.PTS
	err := d.picture(&p.pic)
	select {
	case d.free <- p:
	default:
	}
	if err != nil {
		d.err = err
	}
	return err
}

// Wait hands on pictures until one at or after pts has come (or the
// stream ends, or ffmpeg has given nothing for a while): for a stream fed
// ahead of another whose pictures pair with its.
func (d *ffDecoder) Wait(pts int64) error {
	timeout := time.NewTimer(ffWait)
	defer timeout.Stop()
	for d.err == nil && !d.eof && d.last < pts {
		select {
		case p := <-d.frames:
			if err := d.hand(p); err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			timeout.Reset(ffWait)
		case <-timeout.C:
			return nil
		}
	}
	return nil
}

// ffWait is how long Wait waits for ffmpeg's next picture.
const ffWait = 10 * time.Second

// Close stops ffmpeg.
func (d *ffDecoder) Close() error {
	if d.writes != nil {
		close(d.writes)
		d.writes = nil
	}
	if d.cmd.ProcessState == nil {
		_ = d.cmd.Process.Kill()
		for stopped := false; !stopped; { // let the reader end
			select {
			case <-d.frames:
			case <-d.done:
				stopped = true
			}
		}
		_ = d.cmd.Wait()
	}
	return nil
}

// The transport stream: one program, the video on PID 0x1011 (with the
// HDMV registration, so ffmpeg reads stream type 0xEA as VC-1), and tables
// before every keyframe-sized run of packets.
const (
	ffPMTPID   = 0x100
	ffVideoPID = 0x1011
)

// fed is a caller's timestamp on ffmpeg's timeline: each clip (the bits
// from 40 up) starts ten seconds after the last one's latest picture.
func (d *ffDecoder) fed(pts int64) int64 {
	if pts < 0 {
		return -1
	}
	clip, t := pts>>ptsTagShift, pts&(1<<ptsTagShift-1)
	first, ok := d.clipFirst[clip]
	if !ok {
		first = t
		d.clipFirst[clip] = t
		d.clipStart[clip] = d.fedMax + 10*90000
	}
	f := t - first + d.clipStart[clip]
	d.fedMax = max(d.fedMax, f)
	d.fedPTS[f&(1<<33-1)] = pts
	return f & (1<<33 - 1)
}

func (d *ffDecoder) packetize(au []byte, pts int64) []byte {
	var out bytes.Buffer
	if !d.wrote {
		d.wrote = true
		prog := &m2ts.Program{Number: 1, PCRPID: 0x1fff, Info: []byte{5, 4, 'H', 'D', 'M', 'V'},
			Streams: []m2ts.ProgramStream{{Type: d.streamType, PID: ffVideoPID}}}
		d.section(&out, 0, m2ts.Section(0, 1, 0, m2ts.PATBody(1, ffPMTPID)))
		d.section(&out, ffPMTPID, m2ts.Section(2, 1, 0, m2ts.PMTBody(prog)))
	}
	pes := []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0, 0}
	if f := d.fed(pts); f >= 0 {
		pes[7], pes[8] = 0x80, 5
		pes = append(pes, byte(0x21|f>>29&0x0e), byte(f>>22), byte(f>>14|1), byte(f>>7), byte(f<<1|1))
	}
	if d.streamType == m2ts.TypeVC1 {
		pes[3] = 0xfd // VC-1's extended stream id
	}
	d.packets(&out, ffVideoPID, append(pes, au...))
	return out.Bytes()
}

func (d *ffDecoder) section(out *bytes.Buffer, pid uint16, sec []byte) {
	d.packets(out, pid, append([]byte{0}, sec...))
}

// packets writes a payload as transport packets, padding the last with an
// adaptation field.
func (d *ffDecoder) packets(out *bytes.Buffer, pid uint16, payload []byte) {
	ci := 0
	if pid == ffVideoPID {
		ci = 1
	}
	for first := true; len(payload) > 0; first = false {
		h := []byte{0x47, byte(pid >> 8 & 0x1f), byte(pid), 0x10 | d.cc[ci]&0x0f}
		d.cc[ci]++
		if first {
			h[1] |= 0x40
		}
		n := min(len(payload), 184)
		if n < 184 {
			h[3] |= 0x20
			pad := 184 - n - 1 // adaptation_field_length
			h = append(h, byte(pad))
			if pad > 0 {
				h = append(h, 0)
				h = append(h, bytes.Repeat([]byte{0xff}, pad-1)...)
			}
		}
		out.Write(h)
		out.Write(payload[:n])
		payload = payload[n:]
	}
}

// ffLog keeps ffmpeg's messages: the output stream's format, which says
// how to read the pictures, and the last lines, for an error.
type ffLog struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	lines    []string
	found    chan ffFormat
	sent     bool
	num, den int // the input's frame rate
}

type ffFormat struct {
	w, h, depth int
	color       gpu.ColorInfo
	num, den    int // the input stream's frame rate, when ffmpeg states one
	err         error
}

func newFFLog() *ffLog { return &ffLog{found: make(chan ffFormat, 1)} }

var ffInputRate = regexp.MustCompile(`Stream #0:0.*: Video: .*, ([0-9.]+) fps`)

var ffStreamLine = regexp.MustCompile(`Stream #0:0.*: Video: rawvideo[^,]*, (yuv420p(?:10le)?|yuvj420p)(\(([^)]*)\))?, (\d+)x(\d+)`)

func (l *ffLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(b)
	for {
		i := bytes.IndexByte(l.buf.Bytes(), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(l.buf.Next(i+1)), "\r\n")
		l.lines = append(l.lines, line)
		if len(l.lines) > 20 {
			l.lines = l.lines[1:]
		}
		if l.sent {
			continue
		}
		if m := ffInputRate.FindStringSubmatch(line); m != nil && !strings.Contains(line, "rawvideo") {
			if fps, err := strconv.ParseFloat(m[1], 64); err == nil && fps > 0 {
				l.num, l.den = rateFromDuration(time.Duration(float64(time.Second) / fps))
			}
		}
		if m := ffStreamLine.FindStringSubmatch(line); m != nil {
			w, _ := strconv.Atoi(m[4])
			h, _ := strconv.Atoi(m[5])
			f := ffFormat{w: w, h: h, depth: 8, color: parseFFColor(m[3]), num: l.num, den: l.den}
			if m[1] == "yuv420p10le" {
				f.depth = 10
			}
			l.sent = true
			l.found <- f
		} else if strings.Contains(line, "Video: rawvideo") {
			l.sent = true
			l.found <- ffFormat{err: fmt.Errorf("ffmpeg decodes to a format this does not take: %s", strings.TrimSpace(line))}
		}
	}
	return len(b), nil
}

// format waits for the output stream's format (or ffmpeg's end).
func (l *ffLog) format() (ffFormat, error) {
	f := <-l.found
	return f, f.err
}

// end unblocks format when ffmpeg ends without saying.
func (l *ffLog) end() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.sent {
		l.sent = true
		l.found <- ffFormat{err: errors.New("ffmpeg ended before decoding a picture")}
	}
}

func (l *ffLog) tail() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return ""
	}
	return "\n" + strings.Join(l.lines[max(0, len(l.lines)-5):], "\n")
}

// parseFFColor reads ffmpeg's "tv, bt2020nc/bt2020/smpte2084, progressive"
// (matrix/primaries/transfer; one name when all three agree).
func parseFFColor(s string) gpu.ColorInfo {
	c := gpu.ColorInfo{Primaries: 2, Transfer: 2, Matrix: 2}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "pc":
			c.FullRange = true
		case strings.Contains(part, "/"):
			f := strings.Split(part, "/")
			if len(f) == 3 {
				c.Matrix, c.Primaries, c.Transfer = ffCode(ffMatrices, f[0]), ffCode(ffPrimaries, f[1]), ffCode(ffTransfers, f[2])
			}
		case ffMatrices[part] != 0 || ffPrimaries[part] != 0 || ffTransfers[part] != 0:
			c.Matrix, c.Primaries, c.Transfer = ffCode(ffMatrices, part), ffCode(ffPrimaries, part), ffCode(ffTransfers, part)
		}
	}
	return c
}

func ffCode(m map[string]int, name string) int {
	if v, ok := m[name]; ok {
		return v
	}
	return 2
}

// H.273 code points by ffmpeg's names.
var (
	ffMatrices  = map[string]int{"bt709": 1, "fcc": 4, "bt470bg": 5, "smpte170m": 6, "smpte240m": 7, "bt2020nc": 9, "bt2020c": 10}
	ffPrimaries = map[string]int{"bt709": 1, "bt470m": 4, "bt470bg": 5, "smpte170m": 6, "smpte240m": 7, "film": 8, "bt2020": 9}
	ffTransfers = map[string]int{"bt709": 1, "gamma22": 4, "gamma28": 5, "smpte170m": 6, "smpte240m": 7,
		"bt2020-10": 14, "bt2020-12": 15, "smpte2084": 16, "arib-std-b67": 18}
)
