package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Scheduler runs background work in-process (spec §5). main ticks RunDue
// every 30s; tests call RunDue directly after moving the fake clock.
type Scheduler struct {
	now  func() time.Time
	log  *slog.Logger
	mu   sync.Mutex
	jobs []job
}

type job struct {
	name string
	due  func(now time.Time) bool
	run  func(ctx context.Context, now time.Time) error
}

// Add registers a job. due is asked on every RunDue; run happens when it says yes.
func (s *Scheduler) Add(name string, due func(time.Time) bool, run func(context.Context, time.Time) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, job{name, due, run})
}

// RunDue runs every due job once, in registration order. A failing or
// panicking job is logged and never stops the others.
func (s *Scheduler) RunDue(ctx context.Context) {
	s.mu.Lock()
	jobs := append([]job(nil), s.jobs...)
	s.mu.Unlock()
	now := s.now()
	for _, j := range jobs {
		if j.due(now) {
			if err := runJob(ctx, j, now); err != nil {
				s.log.Error("scheduled job failed", "job", j.name, "err", err)
			}
		}
	}
}

func runJob(ctx context.Context, j job, now time.Time) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return j.run(ctx, now)
}

// Run ticks RunDue every interval until ctx is done.
func (s *Scheduler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RunDue(ctx)
		}
	}
}
