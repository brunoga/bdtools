package hevc

// The video parameter set, read for what decoding a second layer needs
// (MV-HEVC, Annex F/G): the layers, their dependencies, their picture
// formats and buffer sizes. A single-layer stream's VPS has nothing the
// decoder uses.

// repFormat is a rep_format() (F.7.3.2.1.2): a layer's picture format.
type repFormat struct {
	width, height       int
	chromaFormat        int
	sepColour           bool
	bitDepth, bitDepthC int
	confWin             [4]int // left, right, top, bottom, in luma samples
}

// vpsInfo is a VPS's multi-layer description.
type vpsInfo struct {
	id        int
	maxSub    int   // vps_max_sub_layers_minus1
	maxLayers int   // MaxLayersMinus1 + 1
	layerID   []int // layer_id_in_nuh by layer index
	viewID    []int // ViewId by layer index
	// directRef[i] lists the layer indexes layer i predicts from.
	directRef              [][]int
	defaultRefLayersActive bool
	maxOneActiveRefLayer   bool
	pocLsbNotPresent       []bool
	reps                   []repFormat
	repIdx                 []int // by layer index
	// The buffer sizes of each layer (by index), from the first output
	// layer set that holds it; zero when not given.
	maxDecPicBuf, maxReorder, maxLatency []int
}

// layerIdx is a nuh_layer_id's index in the VPS, -1 if it has none.
func (v *vpsInfo) layerIdx(layer int) int {
	for i, id := range v.layerID {
		if id == layer {
			return i
		}
	}
	return -1
}

// skipPTL reads past profile_tier_level(profilePresent, maxSub).
func skipPTL(r *bits, profilePresent bool, maxSub int) {
	if profilePresent {
		r.skip(88)
	}
	r.skip(8) // general_level_idc
	var pp, lp [8]bool
	for i := range maxSub {
		pp[i], lp[i] = r.flag(), r.flag()
	}
	if maxSub > 0 {
		r.skip(2 * (8 - maxSub))
	}
	for i := range maxSub {
		if pp[i] {
			r.skip(88)
		}
		if lp[i] {
			r.skip(8)
		}
	}
}

func ceilLog2(n int) int {
	b := 0
	for 1<<b < n {
		b++
	}
	return b
}

// parseVPS reads a VPS, keeping its multi-layer description when it has
// one.
func (d *Decoder) parseVPS(r *bits) error {
	v := &vpsInfo{id: r.u(4)}
	baseInternal := r.flag()
	r.skip(1) // vps_base_layer_available_flag
	maxLayersMinus1 := r.u(6)
	maxSub := r.u(3)
	v.maxSub = maxSub
	r.skip(1 + 16)
	skipPTL(r, true, maxSub)
	subOrdering := r.flag()
	first := maxSub
	if subOrdering {
		first = 0
	}
	for range maxSub - first + 1 {
		r.ue()
		r.ue()
		r.ue()
	}
	maxLayerID := r.u(6)
	numLayerSets := r.ue() + 1
	if numLayerSets > 1024 {
		return errStream
	}
	layerSets := make([][]int, numLayerSets) // nuh_layer_ids
	layerSets[0] = []int{0}
	for i := 1; i < numLayerSets; i++ {
		for j := 0; j <= maxLayerID; j++ {
			if r.flag() {
				layerSets[i] = append(layerSets[i], j)
			}
		}
	}
	if r.flag() { // vps_timing_info_present_flag
		r.skip(64)
		if r.flag() {
			r.ue()
		}
		n := r.ue()
		if n > 1024 {
			return errStream
		}
		for i := range n {
			r.ue()
			common := true
			if i > 0 {
				common = r.flag()
			}
			parseHRD(r, common, maxSub)
		}
	}
	if !r.flag() || maxLayersMinus1 == 0 { // vps_extension_flag
		d.vps[v.id] = nil
		return nil
	}
	for r.pos&7 != 0 {
		r.skip(1)
	}
	if r.left() < 0 {
		return errShort
	}
	// vps_extension() (F.7.3.2.1.1).
	if baseInternal {
		skipPTL(r, false, maxSub)
	}
	splitting := r.flag()
	var mask [16]bool
	numTypes := 0
	for i := range 16 {
		mask[i] = r.flag()
		if mask[i] {
			numTypes++
		}
	}
	dimLen := make([]int, numTypes)
	for j := range numTypes - boolInt(splitting) {
		dimLen[j] = r.u(3) + 1
	}
	if splitting && numTypes > 0 {
		// The last dimension takes the bits left of the six.
		sum := 0
		for _, l := range dimLen[:numTypes-1] {
			sum += l
		}
		dimLen[numTypes-1] = 6 - sum
	}
	idPresent := r.flag()
	n := maxLayersMinus1 + 1
	v.maxLayers = n
	v.layerID = make([]int, n)
	dimID := make([][]int, n)
	for i := range n {
		dimID[i] = make([]int, numTypes)
		v.layerID[i] = i
		if i == 0 {
			continue
		}
		if idPresent {
			v.layerID[i] = r.u(6)
		}
		if !splitting {
			for j := range numTypes {
				dimID[i][j] = r.u(dimLen[j])
			}
		} else {
			shift := 0
			for j := range numTypes {
				dimID[i][j] = (v.layerID[i] >> shift) & (1<<dimLen[j] - 1)
				shift += dimLen[j]
			}
		}
	}
	// ViewOrderIdx is the scalability dimension of the multiview mask bit
	// (1); NumViews counts its distinct values.
	viewOrder := make([]int, n)
	if mask[1] {
		j := boolInt(mask[0])
		for i := range n {
			viewOrder[i] = dimID[i][j]
		}
	}
	numViews := 1
	for i := 1; i < n; i++ {
		seen := false
		for k := range i {
			if viewOrder[k] == viewOrder[i] {
				seen = true
			}
		}
		if !seen {
			numViews++
		}
	}
	viewIDLen := r.u(4)
	viewIDVal := make([]int, numViews)
	if viewIDLen > 0 {
		for i := range numViews {
			viewIDVal[i] = r.u(viewIDLen)
		}
	}
	v.viewID = make([]int, n)
	for i := range n {
		if viewOrder[i] < numViews {
			v.viewID[i] = viewIDVal[viewOrder[i]]
		}
	}
	direct := make([][]bool, n)
	v.directRef = make([][]int, n)
	for i := range n {
		direct[i] = make([]bool, n)
		for j := range i {
			if i > 0 && r.flag() {
				direct[i][j] = true
				v.directRef[i] = append(v.directRef[i], j)
			}
		}
	}
	independent := 0
	for i := range n {
		if len(v.directRef[i]) == 0 {
			independent++
		}
	}
	if independent > 1 && r.ue() != 0 { // num_add_layer_sets
		return unsupported("a VPS adding layer sets")
	}
	subMax := make([]int, n)
	if r.flag() { // vps_sub_layers_max_minus1_present_flag
		for i := range n {
			subMax[i] = r.u(3)
		}
	} else {
		for i := range n {
			subMax[i] = maxSub
		}
	}
	if r.flag() { // max_tid_ref_present_flag
		for i := range n - 1 {
			for j := i + 1; j < n; j++ {
				if direct[j][i] {
					r.skip(3)
				}
			}
		}
	}
	v.defaultRefLayersActive = r.flag()
	numPTL := r.ue() + 1
	if numPTL > 64 {
		return errStream
	}
	for i := 1 + boolInt(baseInternal); i < numPTL; i++ {
		skipPTL(r, r.flag(), maxSub)
	}
	// The layer sets as layer indexes, and what each needs.
	idx := func(id int) int { return v.layerIdx(id) }
	numOLS := numLayerSets
	defaultOutput := 0
	if numLayerSets > 1 {
		numOLS += r.ue() // num_add_olss
		defaultOutput = r.u(2)
	}
	if numOLS > 1024 {
		return errStream
	}
	olsLS := make([]int, numOLS)
	necessary := make([][]bool, numOLS)
	necessary[0] = []bool{true}
	for i := 1; i < numOLS; i++ {
		olsLS[i] = i
		if numLayerSets > 2 && i >= numLayerSets {
			olsLS[i] = r.u(ceilLog2(numLayerSets-1)) + 1
		}
		if olsLS[i] >= numLayerSets {
			return errStream
		}
		ls := layerSets[olsLS[i]]
		output := make([]bool, len(ls))
		if i >= numLayerSets || defaultOutput == 2 {
			for j := range ls {
				output[j] = r.flag()
			}
		} else {
			for j := range ls {
				output[j] = defaultOutput == 0 || j == len(ls)-1
			}
		}
		// Necessary: the output layers and what they predict from.
		need := make([]bool, len(ls))
		var mark func(k int)
		mark = func(k int) {
			need[k] = true
			li := idx(ls[k])
			if li < 0 {
				return
			}
			for _, rl := range v.directRef[li] {
				for kk, id := range ls {
					if idx(id) == rl && !need[kk] {
						mark(kk)
					}
				}
			}
		}
		numOut, highest := 0, 0
		for j := range ls {
			if output[j] {
				mark(j)
				numOut++
				highest = ls[j]
			}
		}
		necessary[i] = need
		for j := range ls {
			if need[j] && numPTL > 1 {
				r.skip(ceilLog2(numPTL))
			}
		}
		if hi := idx(highest); numOut == 1 && hi >= 0 && len(v.directRef[hi]) > 0 {
			r.skip(1) // alt_output_layer_flag
		}
	}
	numReps := r.ue() + 1
	if numReps > 16 {
		return errStream
	}
	for i := range numReps {
		var f repFormat
		if i > 0 {
			f = v.reps[i-1]
		}
		f.width, f.height = r.u(16), r.u(16)
		if r.flag() {
			f.chromaFormat = r.u(2)
			if f.chromaFormat == 3 {
				f.sepColour = r.flag()
			}
			f.bitDepth = r.u(4) + 8
			f.bitDepthC = r.u(4) + 8
		}
		f.confWin = [4]int{}
		if r.flag() {
			for k := range 4 {
				f.confWin[k] = r.ue() * 2
			}
		}
		v.reps = append(v.reps, f)
	}
	v.repIdx = make([]int, n)
	present := numReps > 1 && r.flag()
	for i := range n {
		v.repIdx[i] = min(i, numReps-1)
	}
	if present {
		for i := boolInt(baseInternal); i < n; i++ {
			v.repIdx[i] = r.u(ceilLog2(numReps))
		}
	}
	v.maxOneActiveRefLayer = r.flag()
	r.skip(1) // vps_poc_lsb_aligned_flag
	v.pocLsbNotPresent = make([]bool, n)
	for i := 1; i < n; i++ {
		if len(v.directRef[i]) == 0 {
			v.pocLsbNotPresent[i] = r.flag()
		}
	}
	// dpb_size() (F.7.3.2.1.3).
	v.maxDecPicBuf = make([]int, n)
	v.maxReorder = make([]int, n)
	v.maxLatency = make([]int, n)
	done := make([]bool, n) // sizes taken from an earlier set
	for i := 1; i < numOLS; i++ {
		ls := layerSets[olsLS[i]]
		maxSubLS := 0
		for _, id := range ls {
			if li := idx(id); li >= 0 {
				maxSubLS = max(maxSubLS, subMax[li])
			}
		}
		flagInfo := r.flag()
		dec := make([]int, len(ls))
		for j := 0; j <= maxSubLS; j++ {
			if j > 0 && flagInfo && !r.flag() {
				continue // as the sub-layer below
			}
			for k, id := range ls {
				if necessary[i][k] && (baseInternal || id != 0) {
					dec[k] = r.ue() + 1
				}
			}
			reorder, lat := r.ue(), r.ue()
			// The highest sub-layer's sizes, for each layer the set holds.
			for k, id := range ls {
				if li := idx(id); li >= 0 && !done[li] && dec[k] > 0 {
					v.maxDecPicBuf[li], v.maxReorder[li], v.maxLatency[li] = dec[k], reorder, 0
					if lat != 0 {
						v.maxLatency[li] = reorder + lat - 1
					}
				}
			}
		}
		for _, id := range ls {
			if li := idx(id); li >= 0 && v.maxDecPicBuf[li] > 0 {
				done[li] = true
			}
		}
	}
	if r.left() < 0 {
		return errShort
	}
	d.vps[v.id] = v
	return nil
}
