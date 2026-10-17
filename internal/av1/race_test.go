//go:build race

package av1

// raceOn is whether the race detector is in; the tests that only check
// the syntax of fixed settings then run one case, the detector making
// them ten times slower for no more coverage.
const raceOn = true
