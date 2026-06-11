package controlplane

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SchedulerStatus struct {
	Enabled   bool      `json:"enabled"`
	Running   bool      `json:"running"`
	Interval  string    `json:"interval"`
	LastTick  time.Time `json:"last_tick,omitempty"`
	LastError string    `json:"last_error,omitempty"`
}

type Scheduler struct {
	store    Store
	runner   *Runner
	now      func() time.Time
	interval time.Duration

	mu       sync.RWMutex
	running  bool
	lastTick time.Time
	lastErr  string
}

func NewScheduler(store Store, runner *Runner, interval time.Duration, now func() time.Time) *Scheduler {
	if interval <= 0 {
		interval = time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &Scheduler{store: store, runner: runner, interval: interval, now: now}
}

func (s *Scheduler) Status() SchedulerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SchedulerStatus{Enabled: true, Running: s.running, Interval: s.interval.String(), LastTick: s.lastTick, LastError: s.lastErr}
}

func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			if err := s.Tick(ctx); err != nil {
				log.Printf("switchyard scheduler tick: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Scheduler) Tick(ctx context.Context) error {
	now := s.now().UTC()
	s.mu.Lock()
	s.lastTick = now
	s.lastErr = ""
	s.mu.Unlock()

	for _, trigger := range s.store.ListTriggers() {
		if !trigger.Enabled || !isScheduledTrigger(trigger.Type) || trigger.Schedule == "" {
			continue
		}
		if trigger.NextRunAt == nil {
			next := nextScheduledTime(trigger.Schedule, now)
			_, _ = s.store.UpdateTriggerScheduleState(trigger.ID, nil, &next, "")
			continue
		}
		if trigger.NextRunAt.After(now) {
			continue
		}
		_, err := s.runner.RunTrigger(ctx, trigger.ID)
		last := now
		next := nextScheduledTime(trigger.Schedule, now.Add(time.Minute))
		lastErr := ""
		if err != nil {
			lastErr = err.Error()
			s.mu.Lock()
			s.lastErr = lastErr
			s.mu.Unlock()
		}
		_, _ = s.store.UpdateTriggerScheduleState(trigger.ID, &last, &next, lastErr)
	}
	return nil
}

func isScheduledTrigger(t TriggerType) bool {
	return t == TriggerCron || t == TriggerPoll
}

func nextScheduledTime(schedule string, after time.Time) time.Time {
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return after.Add(time.Hour)
	}
	for candidate := after.Truncate(time.Minute).Add(time.Minute); candidate.Before(after.Add(366 * 24 * time.Hour)); candidate = candidate.Add(time.Minute) {
		if cronFieldMatches(fields[0], candidate.Minute(), 0, 59) && cronFieldMatches(fields[1], candidate.Hour(), 0, 23) {
			return candidate.UTC()
		}
	}
	return after.Add(time.Hour).UTC()
}

func cronFieldMatches(field string, value int, min int, max int) bool {
	if field == "*" {
		return true
	}
	if strings.HasPrefix(field, "*/") {
		step, err := strconv.Atoi(strings.TrimPrefix(field, "*/"))
		return err == nil && step > 0 && value%step == 0
	}
	exact, err := strconv.Atoi(field)
	return err == nil && exact >= min && exact <= max && value == exact
}
