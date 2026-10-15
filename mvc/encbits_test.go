package mvc

import (
	"math/rand"
	"testing"
)

func TestExpGolombRoundTrip(t *testing.T) {
	var w bitWriter
	vals := []uint32{0, 1, 2, 3, 7, 8, 254, 255, 65535, 1<<31 - 1, 1<<32 - 2}
	svals := []int32{0, 1, -1, 2, -2, 1000, -1000}
	for _, v := range vals {
		w.ue(v)
	}
	for _, v := range svals {
		w.se(v)
	}
	w.u(5, 21)
	rbsp := w.trailing()
	var br bitReader
	br.init(rbsp)
	for _, v := range vals {
		if g := br.ue(); g != v {
			t.Fatalf("ue %d read back as %d", v, g)
		}
	}
	for _, v := range svals {
		if g := br.se(); g != v {
			t.Fatalf("se %d read back as %d", v, g)
		}
	}
	if g := br.u(5); g != 21 {
		t.Fatalf("u(5) read back as %d", g)
	}
}

// TestCABACRoundTrip encodes random decisions, bypass bins and terminates
// and decodes them with the decoder's engine.
func TestCABACRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	type op struct{ kind, ctx, bin int }
	for trial := 0; trial < 50; trial++ {
		var ops []op
		n := 1 + r.Intn(20000)
		skew := r.Float64()
		for i := 0; i < n; i++ {
			k := r.Intn(10)
			switch {
			case k < 7:
				b := 0
				if r.Float64() < skew {
					b = 1
				}
				ops = append(ops, op{0, r.Intn(460), b})
			case k < 9:
				ops = append(ops, op{1, 0, r.Intn(2)})
			default:
				ops = append(ops, op{2, 0, 0})
			}
		}
		initSet, qp := r.Intn(4), r.Intn(52)
		var w bitWriter
		w.u(3, 5) // some header bits, then alignment as in a slice
		w.alignOnes()
		at := len(w.b)
		var e cabacEncoder
		e.start(&w, initSet, qp)
		for _, o := range ops {
			switch o.kind {
			case 0:
				e.decision(o.ctx, o.bin)
			case 1:
				e.bypass(o.bin)
			default:
				e.terminate(0)
			}
		}
		e.terminate(1)
		w.alignZeros()
		buf := append(w.bytes(), make([]byte, cabacPad)...) // as the decoder pads slice data

		var d cabac
		var ctx [512]uint8
		initContexts(&ctx, initSet, qp)
		d.init(buf, at)
		for i, o := range ops {
			var got uint32
			switch o.kind {
			case 0:
				got = d.decision(&ctx[o.ctx])
			case 1:
				got = d.bypass()
			default:
				got = d.terminate()
			}
			if int(got) != o.bin {
				t.Fatalf("trial %d op %d (kind %d): got %d want %d", trial, i, o.kind, got, o.bin)
			}
		}
		if d.terminate() != 1 {
			t.Fatalf("trial %d: end not found", trial)
		}
	}
}
