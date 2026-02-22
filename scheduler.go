package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/containers/podman/v5/pkg/bindings/system"
	"github.com/containers/podman/v5/pkg/domain/entities/types"
	"github.com/rs/zerolog/log"
)

// Scheduler manages healthcheck execution for multiple containers
type Scheduler struct {
	conn       context.Context
	containers map[string]*ScheduledContainer
	mu         sync.RWMutex
	stopCh     chan struct{}
}

// ScheduledContainer represents a container with its healthcheck schedule
type ScheduledContainer struct {
	ID          string
	Name        string
	Interval    time.Duration
	Timeout     time.Duration
	StartPeriod time.Duration
	Retries     int
	stopCh      chan struct{}
	startTime   time.Time
}

// NewScheduler creates a new healthcheck scheduler
func NewScheduler(conn context.Context) *Scheduler {
	return &Scheduler{
		conn:       conn,
		containers: make(map[string]*ScheduledContainer),
		stopCh:     make(chan struct{}),
	}
}

// discoverContainers finds all running containers with healthchecks and adds them to the scheduler
func (s *Scheduler) discoverContainers(ctx context.Context) error {
	logger := log.Ctx(ctx)

	// Only list running containers
	listOpts := &containers.ListOptions{}
	containerList, err := containers.List(s.conn, listOpts)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to list containers during discovery")
		// Don't fail completely, just log and return
		return nil
	}

	for _, ctr := range containerList {
		if err := s.addContainer(ctx, ctr.ID); err != nil {
			logger.Warn().Err(err).Str("container", ctr.ID).Msg("Failed to add container")
		}
	}

	return nil
}

// addContainer adds a container to the scheduler if it has a healthcheck
func (s *Scheduler) addContainer(ctx context.Context, containerID string) error {
	logger := log.Ctx(ctx)

	// Check if already scheduled
	s.mu.RLock()
	if _, exists := s.containers[containerID]; exists {
		s.mu.RUnlock()
		return nil
	}
	s.mu.RUnlock()

	// Inspect to get healthcheck config
	inspectData, err := containers.Inspect(s.conn, containerID, nil)
	if err != nil {
		// Don't spam logs for containers that can't be inspected
		// They might be in the process of being removed
		return fmt.Errorf("failed to inspect container: %w", err)
	}

	if inspectData.Config.Healthcheck == nil || len(inspectData.Config.Healthcheck.Test) == 0 {
		return nil // No healthcheck configured
	}

	interval := time.Duration(inspectData.Config.Healthcheck.Interval)
	timeout := time.Duration(inspectData.Config.Healthcheck.Timeout)
	startPeriod := time.Duration(inspectData.Config.Healthcheck.StartPeriod)
	retries := inspectData.Config.Healthcheck.Retries

	// Default interval if not set
	if interval == 0 {
		interval = 30 * time.Second
	}

	// Default timeout if not set
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	// Get container name
	name := inspectData.Name
	if name == "" {
		name = containerID[:12]
	}

	// Get container start time
	startTime := time.Now()
	if inspectData.State != nil && !inspectData.State.StartedAt.IsZero() {
		startTime = inspectData.State.StartedAt
	}

	scheduled := &ScheduledContainer{
		ID:          containerID,
		Name:        name,
		Interval:    interval,
		Timeout:     timeout,
		StartPeriod: startPeriod,
		Retries:     retries,
		stopCh:      make(chan struct{}),
		startTime:   startTime,
	}

	s.mu.Lock()
	s.containers[containerID] = scheduled
	s.mu.Unlock()

	// Start healthcheck loop for this container
	go s.runHealthcheckLoop(ctx, scheduled)

	logger.Info().
		Str("id", containerID[:12]).
		Str("name", scheduled.Name).
		Dur("interval", interval).
		Dur("start_period", startPeriod).
		Msg("Added container to scheduler")

	return nil
}

// removeContainer removes a container from the scheduler
// Safe to call from any goroutine
func (s *Scheduler) removeContainer(ctx context.Context, containerID string) {
	logger := log.Ctx(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()

	sc, exists := s.containers[containerID]
	if !exists {
		return
	}

	close(sc.stopCh)
	delete(s.containers, containerID)

	logger.Info().
		Str("id", containerID[:12]).
		Str("name", sc.Name).
		Msg("Removed container from scheduler")
}

// Run starts the scheduler and listens for container events
func (s *Scheduler) Run(ctx context.Context) error {
	logger := log.Ctx(ctx)

	// Initial discovery
	if err := s.discoverContainers(ctx); err != nil {
		// Log but don't fail - we can still handle events
		logger.Warn().Err(err).Msg("Initial discovery had issues, continuing anyway")
	}

	s.mu.RLock()
	containerCount := len(s.containers)
	s.mu.RUnlock()

	logger.Info().Int("count", containerCount).Msg("Starting healthcheck scheduler")

	// Start event listener
	eventChan := make(chan types.Event, 10)

	go func() {
		for {
			filters := map[string][]string{
				"type":  {"container"},
				"event": {"start", "die", "stop", "remove"},
			}
			opts := &system.EventsOptions{
				Filters: filters,
			}

			logger.Debug().Msg("Starting event listener")
			err := system.Events(s.conn, eventChan, nil, opts)
			if err != nil {
				select {
				case <-s.stopCh:
					// Scheduler is stopping, exit cleanly
					return
				default:
					logger.Error().Err(err).Msg("Event stream error, retrying in 5s")
					time.Sleep(5 * time.Second)
					continue
				}
			}
		}
	}()

	// Process events
	for {
		select {
		case event := <-eventChan:
			s.handleEvent(ctx, event)
		case <-ctx.Done():
			logger.Info().Msg("Context cancelled, shutting down scheduler")
			s.Stop()
			return ctx.Err()
		case <-s.stopCh:
			logger.Info().Msg("Shutting down scheduler")
			// Stop all container healthcheck loops
			s.mu.Lock()
			for _, sc := range s.containers {
				close(sc.stopCh)
			}
			s.mu.Unlock()
			return nil
		}
	}
}

// handleEvent processes Podman container events
func (s *Scheduler) handleEvent(ctx context.Context, event types.Event) {
	logger := log.Ctx(ctx).With().
		Str("event", event.Status).
		Str("container_id", event.Actor.ID[:12]).
		Logger()

	logger.Debug().Msg("Received container event")

	eventCtx := logger.WithContext(ctx)

	switch event.Status {
	case "start":
		// Container started, add to scheduler
		if err := s.addContainer(eventCtx, event.Actor.ID); err != nil {
			// Only log actual errors, not "no healthcheck" cases
			if err.Error() != "" {
				logger.Debug().Err(err).Msg("Could not add container")
			}
		}
	case "die", "stop", "remove":
		// Container stopped or removed, remove from scheduler
		s.removeContainer(eventCtx, event.Actor.ID)
	}
}

// runHealthcheckLoop runs healthchecks for a single container on its schedule
func (s *Scheduler) runHealthcheckLoop(ctx context.Context, sc *ScheduledContainer) {
	logger := log.Ctx(ctx).With().
		Str("container_id", sc.ID[:12]).
		Str("container_name", sc.Name).
		Logger()

	// Create a context with this logger for all healthchecks
	loopCtx := logger.WithContext(ctx)

	logger.Debug().Dur("interval", sc.Interval).Msg("Starting healthcheck loop")

	ticker := time.NewTicker(sc.Interval)
	defer ticker.Stop()

	// Check if we're still in start period
	if sc.StartPeriod > 0 {
		elapsed := time.Since(sc.startTime)
		if elapsed < sc.StartPeriod {
			remaining := sc.StartPeriod - elapsed
			logger.Debug().Dur("remaining", remaining).Msg("Delaying initial healthcheck due to start period")

			// Wait for start period to complete
			select {
			case <-time.After(remaining):
				// Start period elapsed, proceed
			case <-sc.stopCh:
				logger.Debug().Msg("Stopping healthcheck loop during start period")
				return
			}
		}
	}

	// Run initial healthcheck
	s.executeHealthcheck(loopCtx, sc)

	for {
		select {
		case <-ticker.C:
			s.executeHealthcheck(loopCtx, sc)
		case <-sc.stopCh:
			logger.Debug().Msg("Stopping healthcheck loop")
			return
		}
	}
}

// executeHealthcheck runs a single healthcheck for a container
func (s *Scheduler) executeHealthcheck(ctx context.Context, sc *ScheduledContainer) {
	logger := log.Ctx(ctx)
	logger.Debug().Msg("Running healthcheck")

	checkCtx, cancel := context.WithTimeout(context.Background(), sc.Timeout)
	defer cancel()

	result, err := containers.RunHealthCheck(checkCtx, sc.ID, nil)
	if err != nil {
		logger.Error().Err(err).Msg("Healthcheck execution failed")

		// Verify container still exists
		// Do this synchronously but in a separate goroutine to not block the healthcheck loop
		go func() {
			_, inspectErr := containers.Inspect(s.conn, sc.ID, nil)
			if inspectErr != nil {
				// Container is gone or unreachable
				log.Ctx(ctx).Warn().
					Str("container_id", sc.ID[:12]).
					Msg("Container no longer accessible after healthcheck failure, removing from scheduler")
				s.removeContainer(ctx, sc.ID)
			}
			// If inspectErr is nil, container still exists - the healthcheck will retry on next interval
		}()

		return
	}

	// Log the result
	if len(result.Log) > 0 {
		lastLog := result.Log[len(result.Log)-1]
		logger.Info().
			Str("status", result.Status).
			Int("failing_streak", result.FailingStreak).
			Str("output", lastLog.Output).
			Msg("Healthcheck completed")
	} else {
		logger.Info().
			Str("status", result.Status).
			Int("failing_streak", result.FailingStreak).
			Msg("Healthcheck completed")
	}
}

// Stop stops the scheduler
func (s *Scheduler) Stop() {
	close(s.stopCh)
}
