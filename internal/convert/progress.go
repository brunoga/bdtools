package convert

import (
	"fmt"
	"time"
)

// progressEvery is how often a long decode reports.
const progressEvery = 30 * time.Second

// progress reports a decode-and-encode as it goes: frames done and the rate,
// and, when the source's length is known, the total, the share done and the
// time left.
type progress struct {
	r       *Runner
	verb    string // "decoded", "encoded"
	started time.Time
	last    time.Time
	total   int
	now     func() time.Time
}

func (r *Runner) newProgress(verb string) *progress {
	t := time.Now()
	return &progress{r: r, verb: verb, started: t, last: t, now: time.Now}
}

// begin learns the frame rate (at the first picture) and, with the
// source's length, announces the total.
func (p *progress) begin(num, den int) {
	if p.r.length <= 0 || num <= 0 || den <= 0 {
		return
	}
	p.total = int((p.r.length.Seconds() * float64(num) / float64(den)) + 0.5)
	p.r.Report.Report("about %d frames to %s (%s at %.3f fps)", p.total,
		map[string]string{"decoded": "decode", "encoded": "encode"}[p.verb],
		p.r.length.Round(time.Second), float64(num)/float64(den))
}

// frame counts a picture done, reporting every progressEvery.
func (p *progress) frame(n int) {
	if t := p.now(); t.Sub(p.last) >= progressEvery {
		p.last = t
		p.r.Report.Report("%s", p.line(n, t))
	}
}

// line renders the progress after n frames.
func (p *progress) line(n int, t time.Time) string {
	fps := float64(n) / t.Sub(p.started).Seconds()
	if p.total <= 0 || n >= p.total || fps <= 0 {
		return fmt.Sprintf("%d frames %s (%.1f fps)", n, p.verb, fps)
	}
	left := time.Duration(float64(p.total-n) / fps * float64(time.Second)).Round(time.Second)
	return fmt.Sprintf("%d of %d frames %s (%.1f%%), %.1f fps, %s left",
		n, p.total, p.verb, 100*float64(n)/float64(p.total), fps, left)
}

// rate is the average frame rate after n frames.
func (p *progress) rate(n int) float64 { return float64(n) / time.Since(p.started).Seconds() }
