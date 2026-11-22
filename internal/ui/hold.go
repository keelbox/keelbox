package ui

import "time"

// Hold takes the live line off the screen and keeps it off while fn runs
// (a question to the user), then draws it again. Output written to the
// view meanwhile waits until fn returns.
func (p *Progress) Hold(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clear()
	fn()
	p.draw()
}

// WriteAt is Write for lines that happened at t. A run followed from its
// log replays what happened before the view started; its modules then take
// the time they took, not the time the replay took.
func (p *Progress) WriteAt(t time.Time, b []byte) (int, error) {
	p.mu.Lock()
	now := p.now
	p.now = func() time.Time { return t }
	p.mu.Unlock()
	n, err := p.Write(b)
	p.mu.Lock()
	p.now = now
	p.mu.Unlock()
	return n, err
}

// Leave stops the view without settling the running module, which carries
// on somewhere this view no longer follows, and returns that module and
// its current step ("" when none was running).
func (p *Progress) Leave() (module, step string) {
	if p.stop != nil {
		close(p.stop)
		<-p.done
		p.stop = nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clear()
	if p.cur != nil {
		module, step = p.cur.module, p.cur.step
	}
	p.cur, p.closed = nil, true
	return module, step
}
