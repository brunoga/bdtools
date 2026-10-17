package vmaf

// The VMAF models: libsvm ν-SVR regressions over the six features, as
// libvmaf evaluates them (predict.c and libsvm's svm.cpp): each feature
// rescaled, the support vectors' RBF kernels summed in libsvm's order,
// the prediction rescaled back, transformed and clipped.

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

//go:embed models/vmaf_v0.6.1.json
var model1080 []byte

//go:embed models/vmaf_4k_v0.6.1.json
var model4K []byte

// supportVector is a sparse support vector: its coefficient and its
// values by feature index (1-based, ascending).
type supportVector struct {
	coef  float64
	index []int
	value []float64
}

// Model is a VMAF model.
type Model struct {
	Name               string
	slope, intercept   float64
	slopes, intercepts []float64 // per feature, in feature order
	features           []string
	gamma, rho         float64
	svs                []supportVector
	transform          *struct{ p0, p1, p2 float64 }
	clipLo, clipHi     float64
	clip, outGteIn     bool
	// Transform applies the model's score transform (libvmaf's
	// enable_transform), which its tool leaves off unless asked.
	Transform bool
}

type jsonModel struct {
	ModelDict struct {
		Model          string    `json:"model"`
		FeatureNames   []string  `json:"feature_names"`
		Slopes         []float64 `json:"slopes"`
		Intercepts     []float64 `json:"intercepts"`
		NormType       string    `json:"norm_type"`
		ScoreClip      []float64 `json:"score_clip"`
		ScoreTransform *struct {
			P0, P1, P2 *float64
			OutGteIn   string `json:"out_gte_in"`
		} `json:"score_transform"`
	} `json:"model_dict"`
}

func parseModel(name string, b []byte) (*Model, error) {
	var j jsonModel
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, err
	}
	d := j.ModelDict
	if d.NormType != "linear_rescale" || len(d.Slopes) != len(d.FeatureNames)+1 || len(d.Intercepts) != len(d.Slopes) {
		return nil, fmt.Errorf("vmaf: model %s: unexpected normalisation", name)
	}
	m := &Model{Name: name, slope: d.Slopes[0], intercept: d.Intercepts[0], slopes: d.Slopes[1:], intercepts: d.Intercepts[1:],
		features: d.FeatureNames}
	if len(d.ScoreClip) == 2 {
		m.clip, m.clipLo, m.clipHi = true, d.ScoreClip[0], d.ScoreClip[1]
	}
	if t := d.ScoreTransform; t != nil {
		v := func(p *float64) float64 {
			if p == nil {
				return 0
			}
			return *p
		}
		m.transform = &struct{ p0, p1, p2 float64 }{v(t.P0), v(t.P1), v(t.P2)}
		m.outGteIn = t.OutGteIn == "true"
	}
	sc := bufio.NewScanner(strings.NewReader(d.Model))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	inSV := false
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if !inSV {
			switch f[0] {
			case "gamma":
				m.gamma, _ = strconv.ParseFloat(f[1], 64)
			case "rho":
				m.rho, _ = strconv.ParseFloat(f[1], 64)
			case "SV":
				inSV = true
			}
			continue
		}
		var sv supportVector
		sv.coef, _ = strconv.ParseFloat(f[0], 64)
		for _, kv := range f[1:] {
			k, v, ok := strings.Cut(kv, ":")
			if !ok {
				continue
			}
			i, err1 := strconv.Atoi(k)
			x, err2 := strconv.ParseFloat(v, 64)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("vmaf: model %s: a support vector's %q", name, kv)
			}
			sv.index, sv.value = append(sv.index, i), append(sv.value, x)
		}
		m.svs = append(m.svs, sv)
	}
	if len(m.svs) == 0 || m.gamma == 0 {
		return nil, fmt.Errorf("vmaf: model %s: no support vectors", name)
	}
	return m, nil
}

var (
	models     = map[string]*Model{}
	modelsOnce sync.Once
	modelsErr  error
)

// ModelFor is the model for pictures h lines high: the 4K one above 1080
// lines, else the 1080p one.
func ModelFor(h int) (*Model, error) {
	modelsOnce.Do(func() {
		for name, b := range map[string][]byte{"vmaf_v0.6.1": model1080, "vmaf_4k_v0.6.1": model4K} {
			m, err := parseModel(name, b)
			if err != nil {
				modelsErr = err
				return
			}
			models[name] = m
		}
	})
	if modelsErr != nil {
		return nil, modelsErr
	}
	if h > 1080 {
		return models["vmaf_4k_v0.6.1"], nil
	}
	return models["vmaf_v0.6.1"], nil
}

// Predict is the score of a picture's features, by name.
func (m *Model) Predict(features map[string]float64) float64 {
	x := make([]float64, len(m.features))
	for i, name := range m.features {
		x[i] = float64(m.slopes[i]*features[name]) + m.intercepts[i] // no fused multiply-adds: libsvm rounds each product
	}
	var sum float64
	for _, sv := range m.svs {
		sum += float64(sv.coef * m.kernel(x, sv))
	}
	p := sum - m.rho
	p = (p - m.intercept) / m.slope
	if t := m.transform; t != nil && m.Transform {
		out := t.p0 + float64(t.p1*p) + float64(t.p2*p*p)
		if m.outGteIn && out < p {
			out = p
		}
		p = out
	}
	if m.clip {
		p = min(max(p, m.clipLo), m.clipHi)
	}
	return p
}

// kernel is libsvm's RBF kernel of the dense x (indices 1..n) and a
// sparse support vector, the squared distance summed in libsvm's order.
func (m *Model) kernel(x []float64, sv supportVector) float64 {
	var sum float64
	i, k := 0, 0
	for i < len(x) && k < len(sv.index) {
		xi := i + 1
		switch {
		case xi == sv.index[k]:
			d := x[i] - sv.value[k]
			sum += float64(d * d)
			i++
			k++
		case xi > sv.index[k]:
			sum += float64(sv.value[k] * sv.value[k])
			k++
		default:
			sum += float64(x[i] * x[i])
			i++
		}
	}
	for ; i < len(x); i++ {
		sum += float64(x[i] * x[i])
	}
	for ; k < len(sv.index); k++ {
		sum += float64(sv.value[k] * sv.value[k])
	}
	return math.Exp(-m.gamma * sum)
}
