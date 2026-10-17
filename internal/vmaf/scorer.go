package vmaf

import (
	"math"
	"runtime"
	"sort"
	"sync"
)

// Score is a picture's measures.
type Score struct {
	VMAF    float64
	PSNR    float64 // luma, dB
	SSIM    float64
	ADM2    float64
	Motion2 float64
	VIF     [4]float64
}

// Scorer measures a distorted video against its reference, a picture
// pair at a time, on every CPU: the pictures are measured as they come,
// VMAF once the next picture's motion is known (Scores).
type Scorer struct {
	model   *Model
	jobs    chan job
	wg      sync.WaitGroup
	mu      sync.Mutex
	scores  []Score
	sads    []float64
	prevRef Frame
	n       int
}

type job struct {
	i              int
	ref, dis, prev Frame
}

// NewScorer starts measuring pictures h lines high (the model chosen by
// the height).
func NewScorer(h int) (*Scorer, error) {
	m, err := ModelFor(h)
	if err != nil {
		return nil, err
	}
	s := &Scorer{model: m, jobs: make(chan job, runtime.GOMAXPROCS(0))}
	for range runtime.GOMAXPROCS(0) {
		s.wg.Add(1)
		go s.work()
	}
	return s, nil
}

func (s *Scorer) work() {
	defer s.wg.Done()
	for j := range s.jobs {
		var sc Score
		sc.VIF = vifScores(j.ref, j.dis)
		sc.ADM2 = adm2(j.ref, j.dis)
		sc.PSNR = PSNR(j.ref, j.dis)
		sc.SSIM = SSIM(j.ref, j.dis)
		sad := 0.0
		if j.i > 0 {
			sad = motionSAD(j.prev, j.ref)
		}
		s.mu.Lock()
		s.scores[j.i] = sc
		s.sads[j.i] = sad
		s.mu.Unlock()
	}
}

// Add measures the next picture pair. The pictures must not change
// after (they are read meanwhile, and the reference is the next's
// previous).
func (s *Scorer) Add(ref, dis Frame) {
	s.mu.Lock()
	s.scores = append(s.scores, Score{})
	s.sads = append(s.sads, 0)
	s.mu.Unlock()
	s.jobs <- job{i: s.n, ref: ref, dis: dis, prev: s.prevRef}
	s.prevRef = ref
	s.n++
}

// Scores waits for the pictures' measures and gives them, VMAF
// included; the Scorer is done.
func (s *Scorer) Scores() []Score {
	close(s.jobs)
	s.wg.Wait()
	for i := range s.scores {
		m2 := 0.0
		if i > 0 {
			m2 = s.sads[i]
			if i+1 < len(s.sads) {
				m2 = math.Min(s.sads[i], s.sads[i+1])
			}
		}
		sc := &s.scores[i]
		sc.Motion2 = m2
		sc.VMAF = s.model.Predict(map[string]float64{
			"VMAF_integer_feature_adm2_score":       sc.ADM2,
			"VMAF_integer_feature_motion2_score":    m2,
			"VMAF_integer_feature_vif_scale0_score": sc.VIF[0],
			"VMAF_integer_feature_vif_scale1_score": sc.VIF[1],
			"VMAF_integer_feature_vif_scale2_score": sc.VIF[2],
			"VMAF_integer_feature_vif_scale3_score": sc.VIF[3],
		})
	}
	return s.scores
}

// Pooled is a measure over a video: its mean, its harmonic mean (VMAF's
// usual pooling for a single number weighs the bad pictures more), the
// worst picture's and the 1st percentile's (from the worst).
type Pooled struct {
	Mean, Harmonic, Min, P1 float64
}

// Pool pools a measure of the pictures.
func Pool(v []float64) Pooled {
	if len(v) == 0 {
		return Pooled{}
	}
	var sum, inv float64
	sorted := append([]float64(nil), v...)
	sort.Float64s(sorted)
	for _, x := range v {
		sum += x
		inv += 1 / (x + 1)
	}
	n := float64(len(v))
	return Pooled{Mean: sum / n, Harmonic: n/inv - 1, Min: sorted[0], P1: sorted[len(sorted)/100]}
}
