package convert

// stagePipe runs functions in the order they are given, on a goroutine of
// its own, a few behind the one giving them: the second half of a
// picture's work beside the first half of the next one's. After an error,
// the ones still to run are dropped.
type stagePipe struct {
	jobs chan func() error
	done chan struct{}
	err  error // the first error, read after done or by push
	errc chan error
}

// stageDepth is how many pictures may wait for the second half.
const stageDepth = 2

func newStagePipe() *stagePipe {
	p := &stagePipe{jobs: make(chan func() error, stageDepth), done: make(chan struct{}), errc: make(chan error, 1)}
	go func() {
		defer close(p.done)
		for f := range p.jobs {
			if p.err != nil {
				continue
			}
			if err := f(); err != nil {
				p.err = err
				p.errc <- err
			}
		}
	}()
	return p
}

// push queues f, giving the error of one run before, if there was one.
func (p *stagePipe) push(f func() error) error {
	select {
	case err := <-p.errc:
		p.errc <- err
		return err
	default:
	}
	p.jobs <- f
	return nil
}

// close runs what is queued and gives the first error.
func (p *stagePipe) close() error {
	close(p.jobs)
	<-p.done
	return p.err
}
