package controlplane

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type ExecutorLimiter interface {
	Acquire(ctx context.Context, executor string) (func(), error)
}

type NoopExecutorLimiter struct{}

func (NoopExecutorLimiter) Acquire(context.Context, string) (func(), error) {
	return func() {}, nil
}

type SemaphoreExecutorLimiter struct {
	limits map[string]chan struct{}
}

func NewSemaphoreExecutorLimiter(limits map[string]int) *SemaphoreExecutorLimiter {
	parsed := map[string]chan struct{}{}
	for executor, limit := range limits {
		executor = strings.TrimSpace(executor)
		if executor == "" || limit <= 0 {
			continue
		}
		parsed[executor] = make(chan struct{}, limit)
	}
	return &SemaphoreExecutorLimiter{limits: parsed}
}

func NewEnvExecutorLimiter() ExecutorLimiter {
	limits := parseExecutorLimits(os.Getenv("SWITCHYARD_EXECUTOR_LIMITS"))
	if len(limits) == 0 {
		return NoopExecutorLimiter{}
	}
	return NewSemaphoreExecutorLimiter(limits)
}

func parseExecutorLimits(raw string) map[string]int {
	limits := map[string]int{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		limit, err := strconv.Atoi(value)
		if err != nil || name == "" || limit <= 0 {
			continue
		}
		limits[name] = limit
	}
	return limits
}

func (l *SemaphoreExecutorLimiter) Acquire(ctx context.Context, executor string) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	semaphore := l.limits[strings.TrimSpace(executor)]
	if semaphore == nil {
		return func() {}, nil
	}
	select {
	case semaphore <- struct{}{}:
		released := false
		return func() {
			if released {
				return
			}
			released = true
			<-semaphore
		}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for %s executor slot: %w", executor, ctx.Err())
	}
}
