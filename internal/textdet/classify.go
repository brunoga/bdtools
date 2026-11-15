package textdet

import (
	_ "embed"
	"encoding/json"
	"math"
)

// overlayTrees is the classifier of overlay text against the text written
// on things in a film: trained on the openings (overlay) and a minute ten
// minutes in (scene) of 98 films (tools/titletrain).
//
//go:embed overlay.json
var overlayTrees []byte

// OverlayTrees is the built-in classifier.
func OverlayTrees() *Trees {
	t, err := LoadTrees(overlayTrees)
	if err != nil {
		panic("textdet: the built-in classifier: " + err.Error())
	}
	return t
}

// Trees is a gradient-boosted classifier (scikit-learn's
// HistGradientBoostingClassifier, exported by tools/titletrain): the
// probability is the logistic of Base plus each tree's leaf value.
type Trees struct {
	Base  float64  `json:"base"`
	Cols  []int    `json:"cols"` // the features the trees read, by their index here
	Trees [][]Node `json:"trees"`
}

// Node is a tree's node: a leaf's Value, or a split on feature F (an
// index into Cols) at T, NaN going left when NaNLeft is set.
type Node struct {
	Leaf    bool    `json:"leaf,omitempty"`
	Value   float64 `json:"v,omitempty"`
	F       int     `json:"f,omitempty"`
	T       float64 `json:"t,omitempty"`
	NaNLeft bool    `json:"nl,omitempty"`
	Lo      int     `json:"l,omitempty"`
	Hi      int     `json:"r,omitempty"`
}

// LoadTrees reads a classifier exported as JSON.
func LoadTrees(b []byte) (*Trees, error) {
	var t Trees
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Prob is the classifier's probability for a region's features.
func (t *Trees) Prob(f [numFeatures]float32) float64 {
	s := t.Base
	for _, tree := range t.Trees {
		i := 0
		for !tree[i].Leaf {
			n := &tree[i]
			v := float64(f[t.Cols[n.F]])
			switch {
			case math.IsNaN(v):
				if n.NaNLeft {
					i = n.Lo
				} else {
					i = n.Hi
				}
			case v <= n.T:
				i = n.Lo
			default:
				i = n.Hi
			}
		}
		s += tree[i].Value
	}
	return 1 / (1 + math.Exp(-s))
}
