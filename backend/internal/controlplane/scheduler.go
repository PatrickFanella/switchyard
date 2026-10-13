package controlplane

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SchedulerStatus struct {
	Enabled         bool      `json:"enabled"`
	Running         bool      `json:"running"`
	Paused          bool      `json:"paused"`
	Interval        string    `json:"interval"`
	LastTick        time.Time `json:"last_tick,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	Timezone        string    `json:"timezone"`
	MissedRunPolicy string    `json:"missed_run_policy"`
}

type Scheduler struct {
	store    Store
	runner   *Runner
	now      func() time.Time
	interval time.Duration
	async    bool
	location *time.Location
	assigner *OpenPilotAssigner
	overseer *OpenPilotOverseer

	mu       sync.RWMutex
	running  bool
	paused   bool
	lastTick time.Time
	lastErr  string
	done     chan struct{}
}

func (s *Scheduler) SetOpenPilotAssigner(assigner *OpenPilotAssigner) { s.assigner = assigner }
func (s *Scheduler) SetOpenPilotOverseer(overseer *OpenPilotOverseer) { s.overseer = overseer }

// SetPaused suspends or resumes scheduled dispatch without stopping the
// scheduler loop. The flag is process-local and is not persisted.
func (s *Scheduler) SetPaused(paused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = paused
}

// Paused reports whether scheduled dispatch is currently suspended.
func (s *Scheduler) Paused() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.paused
}

func (s *Scheduler) SetAsyncDispatch(enabled bool) {
	s.async = enabled
}

func NewScheduler(store Store, runner *Runner, interval time.Duration, now func() time.Time) *Scheduler {
	if interval <= 0 {
		interval = time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &Scheduler{store: store, runner: runner, interval: interval, now: now, location: time.UTC}
}

func (s *Scheduler) SetLocation(location *time.Location) {
	if location == nil {
		location = time.UTC
	}
	s.location = location
}

func (s *Scheduler) Status() SchedulerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SchedulerStatus{Enabled: true, Running: s.running, Paused: s.paused, Interval: s.interval.String(), LastTick: s.lastTick, LastError: s.lastErr, Timezone: s.location.String(), MissedRunPolicy: "coalesce_once"}
}

func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.done = make(chan struct{})
	s.mu.Unlock()

	go func() {
		var overseerWG sync.WaitGroup
		if s.overseer != nil {
			overseerWG.Add(1)
			go func() {
				defer overseerWG.Done()
				ticker := time.NewTicker(s.interval)
				defer ticker.Stop()
				for {
					if !s.Paused() {
						if err := s.overseer.Tick(ctx, false, false); err != nil && err != ErrConflict && ctx.Err() == nil {
							slog.Error("Open Pilot overseer check failed", "error", RedactForLog(err.Error()))
						}
					}
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			}()
		}
		defer func() {
			overseerWG.Wait()

			s.mu.Lock()
			s.running = false
			close(s.done)
			s.mu.Unlock()
		}()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			if err := s.Tick(ctx); err != nil {
				slog.Error("scheduler tick failed", "error", RedactForLog(err.Error()))
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Scheduler) Wait(ctx context.Context) error {
	s.mu.RLock()
	done := s.done
	s.mu.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Scheduler) Tick(ctx context.Context) error {
	now := s.now().UTC()
	s.mu.Lock()
	s.lastTick = now
	s.lastErr = ""
	paused := s.paused
	s.mu.Unlock()
	if paused {
		return nil
	}

	for _, trigger := range s.store.ListTriggers() {
		if !trigger.Enabled || !isScheduledTrigger(trigger.Type) || trigger.Schedule == "" {
			continue
		}
		if trigger.NextRunAt == nil {
			next, err := nextScheduledTime(trigger.Schedule, now, s.location)
			if err != nil {
				_, _ = s.store.UpdateTriggerScheduleState(trigger.ID, nil, nil, err.Error())
				s.mu.Lock()
				s.lastErr = err.Error()
				s.mu.Unlock()
				continue
			}
			_, _ = s.store.UpdateTriggerScheduleState(trigger.ID, nil, &next, "")
			continue
		}
		if trigger.NextRunAt.After(now) {
			continue
		}
		var err error
		if trigger.ID == "gitea-open-pilot-poll" {
			if s.assigner == nil {
				err = fmt.Errorf("Open Pilot assigner is not configured")
			} else {
				_, err = s.assigner.Poll(ctx)
			}
		} else if s.async {
			_, err = s.runner.EnqueueTrigger(trigger.ID)
		} else {
			_, err = s.runner.RunTrigger(ctx, trigger.ID)
		}
		last := now
		next, scheduleErr := nextScheduledTime(trigger.Schedule, now, s.location)
		lastErr := ""
		if err != nil {
			lastErr = err.Error()
			s.mu.Lock()
			s.lastErr = lastErr
			s.mu.Unlock()
		}
		if scheduleErr != nil {
			lastErr = scheduleErr.Error()
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

func nextScheduledTime(schedule string, after time.Time, location *time.Location) (time.Time, error) {
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return time.Time{}, fmt.Errorf("cron schedule must contain five fields")
	}
	if location == nil {
		location = time.UTC
	}
	parsed := make([]cronField, 5)
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for index, bounds := range ranges {
		field, err := parseCronField(fields[index], bounds[0], bounds[1])
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid cron field %d: %w", index+1, err)
		}
		parsed[index] = field
	}
	start := after.In(location)
	for candidate := start.Truncate(time.Minute).Add(time.Minute); candidate.Before(start.Add(5 * 366 * 24 * time.Hour)); candidate = candidate.Add(time.Minute) {
		weekday := int(candidate.Weekday())
		domMatch := parsed[2].matches(candidate.Day())
		dowMatch := parsed[4].matches(weekday) || (weekday == 0 && parsed[4].matches(7))
		dayMatch := domMatch && dowMatch
		if !parsed[2].wildcard && !parsed[4].wildcard {
			dayMatch = domMatch || dowMatch
		}
		if parsed[0].matches(candidate.Minute()) && parsed[1].matches(candidate.Hour()) && dayMatch && parsed[3].matches(int(candidate.Month())) {
			return candidate.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cron schedule has no occurrence within five years")
}

type cronField struct {
	values   map[int]struct{}
	wildcard bool
}

func (f cronField) matches(value int) bool {
	_, ok := f.values[value]
	return ok
}

func parseCronField(raw string, minValue, maxValue int) (cronField, error) {
	field := cronField{values: map[int]struct{}{}, wildcard: raw == "*"}
	parts := strings.Split(raw, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return cronField{}, fmt.Errorf("empty list item")
		}
		base, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			parsed, err := strconv.Atoi(stepText)
			if err != nil || parsed < 1 {
				return cronField{}, fmt.Errorf("invalid step %q", stepText)
			}
			step = parsed
		}
		start, end := minValue, maxValue
		switch {
		case base == "*":
			field.wildcard = field.wildcard || len(parts) == 1
		case strings.Contains(base, "-"):
			startText, endText, ok := strings.Cut(base, "-")
			if !ok {
				return cronField{}, fmt.Errorf("invalid range %q", base)
			}
			var err error
			start, err = strconv.Atoi(startText)
			if err != nil {
				return cronField{}, fmt.Errorf("invalid range start %q", startText)
			}
			end, err = strconv.Atoi(endText)
			if err != nil {
				return cronField{}, fmt.Errorf("invalid range end %q", endText)
			}
		default:
			value, err := strconv.Atoi(base)
			if err != nil {
				return cronField{}, fmt.Errorf("invalid value %q", base)
			}
			if hasStep {
				return cronField{}, fmt.Errorf("step requires wildcard or range")
			}
			start, end = value, value
		}
		if start < minValue || end > maxValue || start > end {
			return cronField{}, fmt.Errorf("value outside %d-%d", minValue, maxValue)
		}
		for value := start; value <= end; value += step {
			field.values[value] = struct{}{}
		}
	}
	if len(field.values) == 0 {
		return cronField{}, fmt.Errorf("field has no values")
	}
	return field, nil
}
