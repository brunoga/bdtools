//go:build race

package av1

// raceOn is whether the race detector is in; the tests that only check
// the syntax of fixed settings then run one case, the detector making
// them ten times slower for no more coverage.
const raceOn = true

// With the detector in, the search is lighter: fewer candidates coded in
// full and the first four references, the goroutines and what they share
// the same.
func init() {
	interCands, obmcCands, warpCands, lumaCands = 2, 1, 1, 2
	dbgFewRefs = true
}
