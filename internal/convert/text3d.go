package convert

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/internal/textdet"
)

// textNet is the text detector --to-3d lifts the titles with: PaddleOCR's
// PP-OCRv4 detection network, as RapidOCR publishes it in ONNX
// (Apache-2.0), downloaded into the user's cache the first time.
var textNet = depthNet{"PaddleOCR PP-OCRv4 text detection", "ch_PP-OCRv4_det_infer.onnx",
	"https://huggingface.co/SWHL/RapidOCR/resolve/main/PP-OCRv4/ch_PP-OCRv4_det_infer.onnx",
	"d2a7720d45a54257208b1e13e36a8479894cb74155a5efe29462512d42f49da9", "4.7 MB", ""}

// text3DWanted reports whether the conversion may lift text anywhere: the
// command's --3d-text, or a stretch's text= (--3d-shots), or the grading,
// which can turn it on.
func (o Options) text3DWanted() bool {
	return textMode(o.Text3D) != stereo.TextOff || o.Shots3D != "" || o.Grade3D != ""
}

// textLift is the text lift, the detector downloaded the first time.
func (r *Runner) textLift() (*stereo.TextLift, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("--to-3d has no cache directory for the text detector (%w). Turn the text lift off with --3d-text off", err)
	}
	file := filepath.Join(dir, "bdtools", textNet.file)
	if _, err := os.Stat(file); err != nil {
		r.Report.Report("--to-3d: downloading the text detector (%s, %s) to %s", textNet.name, textNet.size, file)
		if err := download(textNet.url, file, textNet.sha256); err != nil {
			return nil, fmt.Errorf("--to-3d could not download the text detector: %w. Turn the text lift off with --3d-text off", err)
		}
	}
	b, err := os.ReadFile(file) //nolint:gosec // the cache's file
	if err != nil {
		return nil, err
	}
	g, err := nn.LoadONNX(b)
	if err != nil {
		return nil, fmt.Errorf("the text detector %s: %w", file, err)
	}
	net, err := textdet.Compile(g)
	if err != nil {
		return nil, fmt.Errorf("the text detector %s: %w", file, err)
	}
	start := r.Opts.Trim.Start
	t := &stereo.TextLift{Det: &textdet.Detector{Net: net, Trees: textdet.OverlayTrees()}, Opening: stereo.DefaultOpening}
	t.Clock = func(pts int64) time.Duration {
		if r.timeline == nil {
			return time.Duration(pts) * time.Second / 90000
		}
		return r.timeline(pts) + start
	}
	return t, nil
}
