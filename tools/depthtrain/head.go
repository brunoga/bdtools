package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/brunoga/bdtools/internal/depth"
	"github.com/brunoga/bdtools/internal/nn"
)

// Learning the depth network's head (-head CHECKPOINT): a set of its
// weights (-learn: the tail, the fusion path to the coarsest features,
// or the whole head with its temporal modules) from the pictures'
// windows (NAME.bhw: the picture and those before it as the network took
// them, stereobench -samples -window). The encoder, frozen, runs on each
// frame of the window; the head runs on the frames before the picture's
// for their temporal states (the window padded as the model pads a
// stream's start, so the picture is the stream's K-th frame); then on
// the picture's frame on a tape, the loss tail.go's.

// windowSample is a sample with its window.
type windowSample struct {
	*sample
	h, w   int      // the network's input
	frames [][]byte // the window's frames as the network took them (RGB bytes), oldest last
}

func (ws *windowSample) base() *sample { return ws.sample }

func readWindow(path string) (*windowSample, error) {
	s, err := read(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(strings.TrimSuffix(path, ".bds") + ".bhw") //nolint:gosec // the operator's files
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 1<<20)
	var hdr struct {
		Magic   [4]byte
		K, H, W uint32
	}
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil || string(hdr.Magic[:]) != "BHW1" {
		return nil, fmt.Errorf("%s: not a window", path)
	}
	if hdr.K == 0 || hdr.K > 64 || hdr.H%14 != 0 || hdr.W%14 != 0 {
		return nil, fmt.Errorf("%s: a bad window", path)
	}
	ws := &windowSample{sample: s, h: int(hdr.H), w: int(hdr.W)}
	for range hdr.K {
		fr := make([]byte, 3*ws.h*ws.w)
		if _, err := io.ReadFull(r, fr); err != nil {
			return nil, err
		}
		ws.frames = append(ws.frames, fr)
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return nil, fmt.Errorf("%s: more than %d frames of %dx%d", path, hdr.K, hdr.W, hdr.H)
	}
	return ws, nil
}

// input is frame i as the network takes it ([3, H, W]): the bytes'
// normalisation (stereo's netInput's).
func (ws *windowSample) input(i int) *nn.Tensor {
	mean := [3]float32{0.485, 0.456, 0.406}
	std := [3]float32{0.229, 0.224, 0.225}
	x := nn.New(3, ws.h, ws.w)
	n := ws.h * ws.w
	for c := range 3 {
		for j, b := range ws.frames[i][c*n : (c+1)*n] {
			x.Data[c*n+j] = (float32(b)/255 - mean[c]) / std[c]
		}
	}
	return x
}

// learnSets are -learn's: the layers learnt.
var learnSets = map[string]func(all map[string]*nn.Tensor) []string{
	"tail": func(all map[string]*nn.Tensor) []string { return expand(all, depth.TailNames) },
	"fusion3": func(all map[string]*nn.Tensor) []string {
		layers := append(slices.Clone(depth.TailNames),
			"head.projects.2", "head.scratch.layer3_rn",
			"head.scratch.refinenet3.resConfUnit1.conv1", "head.scratch.refinenet3.resConfUnit1.conv2",
			"head.scratch.refinenet3.resConfUnit2.conv1", "head.scratch.refinenet3.resConfUnit2.conv2",
			"head.scratch.refinenet3.out_conv")
		return expand(all, layers)
	},
	"fusion": func(all map[string]*nn.Tensor) []string {
		layers := append(slices.Clone(depth.TailNames),
			"head.projects.2", "head.scratch.layer3_rn",
			"head.projects.3", "head.resize_layers.3", "head.scratch.layer4_rn",
			"head.scratch.refinenet3.resConfUnit1.conv1", "head.scratch.refinenet3.resConfUnit1.conv2",
			"head.scratch.refinenet3.resConfUnit2.conv1", "head.scratch.refinenet3.resConfUnit2.conv2",
			"head.scratch.refinenet3.out_conv",
			"head.scratch.refinenet4.resConfUnit2.conv1", "head.scratch.refinenet4.resConfUnit2.conv2",
			"head.scratch.refinenet4.out_conv")
		return expand(all, layers)
	},
	"head": func(all map[string]*nn.Tensor) []string {
		var names []string
		for n := range all {
			// not the position encodings (buffers) nor the first fusion
			// block's skip unit (it has no skip)
			if strings.HasPrefix(n, "head.") && !strings.Contains(n, "pos_encoder") && !strings.Contains(n, "refinenet4.resConfUnit1") {
				names = append(names, n)
			}
		}
		slices.Sort(names)
		return names
	},
}

// learnSet is -learn's weights: a set by name, or blocksN, the whole
// head and the encoder's last N blocks with its norm.
func learnSet(learn string, all map[string]*nn.Tensor) ([]string, bool) {
	if set, ok := learnSets[learn]; ok {
		return set(all), true
	}
	var n int
	if _, err := fmt.Sscanf(learn, "blocks%d", &n); err != nil || n < 1 || fmt.Sprintf("blocks%d", n) != learn {
		return nil, false
	}
	blocks := 0
	for all[fmt.Sprintf("pretrained.blocks.%d.norm1.weight", blocks)] != nil {
		blocks++
	}
	if n > blocks {
		return nil, false
	}
	names := learnSets["head"](all)
	for name := range all {
		var b int
		if _, err := fmt.Sscanf(name, "pretrained.blocks.%d.", &b); err == nil && b >= blocks-n {
			names = append(names, name)
		} else if strings.HasPrefix(name, "pretrained.norm.") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names, true
}

// headMode learns a set of the head's weights from the pictures' windows.
type headMode struct {
	hm  *headModel
	m   *depth.Model // the whole network on the GPU, its learnt weights the model's
	enc bool         // some of the encoder's weights are learnt: the picture's encoder on the tape
}

func (md headMode) read(path string) (item, error) { return readWindow(path) }

// updated puts the learnt weights, changed, on the GPU again.
func (md headMode) updated() {
	if err := md.m.SetTail(md.hm.w); err != nil {
		fail(err)
	}
}

// states are the temporal states of the frames before the picture's,
// the head run on each with the weights as they are.
func (md headMode) states(g *nn.GPU, ws *windowSample) [][8]depth.KV {
	var states [][8]depth.KV
	ph, pw := ws.h/14, ws.w/14
	for i := range len(ws.frames) - 1 {
		feats := md.m.Encode(g.Upload(ws.input(i)))
		_, s := depth.Head(g, md.m, feats, ph, pw, depth.StartWindow(states), nil)
		states = append(states, s)
	}
	return states
}

// forward is the head on the picture's frame, on the tape, after the
// frames before it: the depth at the sample's size.
func (md headMode) forward(g *nn.GPU, tp *nn.Tape, learn func(string) bool, ws *windowSample) *nn.Tensor {
	win := depth.StartWindow(md.states(g, ws))
	p := depth.TapeParams(tp, md.m.Weights(), learn)
	x := g.Upload(ws.input(len(ws.frames) - 1))
	var feats [4]*nn.Tensor
	if md.enc {
		feats = depth.Encoder(tp, p, x)
	} else {
		feats = md.m.Encode(x)
	}
	d, _ := depth.Head(tp, p, feats, ws.h/14, ws.w/14, win, nil)
	return tp.Bilinear(d, ws.sample.h, ws.sample.w)
}

func (md headMode) output(g *nn.GPU, it item) []float32 {
	defer g.Release()
	out, err := g.Download(md.forward(g, nn.NewTape(g), none, it.(*windowSample)))
	if err != nil {
		fail(err)
	}
	return out.Data
}

func (md headMode) step(g *nn.GPU, it item, train bool, fixed *[2]float64) (float64, bool) {
	defer g.Release()
	ws := it.(*windowSample)
	learn := none
	if train {
		learn = md.hm.isLearnt
	}
	tp := nn.NewTape(g)
	d := md.forward(g, tp, learn, ws)
	return md.hm.finish(g, tp, d, ws.sample, train, fixed)
}

// trainHead is main's for -head.
func trainHead(ckpt, learn, from, trainDir, valDir, out string, epochs int, lr float64, batch, limit int, fitBy string, half bool) {
	g, err := nn.NewGPU()
	if err != nil {
		fail(err)
	}
	defer g.Close()
	m, err := depth.Load(ckpt)
	if err != nil {
		fail(err)
	}
	names, ok := learnSet(learn, m.Weights())
	if !ok {
		fail(fmt.Errorf("-learn %q: tail, fusion3, fusion, head or blocksN (N the encoder's last blocks, learnt with the head)", learn))
	}
	hm, err := newHeadModel(ckpt, names)
	if err != nil {
		fail(err)
	}
	g.Half = half && m.HalfSafe()
	m.Use(g)
	if from != "" {
		// start from these weights (a tail file's, any of the network's
		// names): the model's, and the learnt ones' starting point
		w, err := depth.ReadTail(from)
		if err != nil {
			fail(err)
		}
		if err := m.SetTail(w); err != nil {
			fail(err)
		}
		for _, n := range hm.names {
			if t, ok := w[n]; ok {
				hm.w[n] = t.Clone()
				hm.w0[n] = t
				hm.all[n] = hm.w[n]
			}
		}
	}
	var enc bool
	for _, n := range names {
		enc = enc || strings.HasPrefix(n, "pretrained.")
	}
	md := headMode{hm, m, enc}
	md.updated()
	var params int
	for _, n := range hm.names {
		params += len(hm.w[n].Data)
	}
	fmt.Printf("the %s: %d weights' tensors, %d values, on %s\n", learn, len(hm.names), params, g.Name())
	trainLoop(g, hm, md, trainDir, valDir, out, epochs, lr, batch, limit, fitBy)
}
