package main

import (
	"context"
	"fmt"
	"time"

	"github.com/containers/podman/v5/pkg/bindings"
	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/rs/zerolog/log"
)

func runDaemon(socket string, ctx context.Context) error {
	// Connect to Podman
	conn, err := bindings.NewConnection(ctx, fmt.Sprintf("unix://%s", socket))
	if err != nil {
		return fmt.Errorf("failed to connect to Podman socket: %w", err)
	}

	log.Ctx(ctx).Info().Str("socket", socket).Msg("Connected to Podman")

	// Create scheduler
	scheduler := NewScheduler(conn)

	// Run the scheduler
	return scheduler.Run(ctx)
}

func runCheck(socket, container string) error {
	ctx := context.Background()

	// Connect to Podman
	conn, err := bindings.NewConnection(ctx, fmt.Sprintf("unix://%s", socket))
	if err != nil {
		return fmt.Errorf("failed to connect to Podman socket: %w", err)
	}

	log.Info().Str("container", container).Msg("Running healthcheck")

	// Run healthcheck
	result, err := containers.RunHealthCheck(conn, container, nil)
	if err != nil {
		return fmt.Errorf("healthcheck failed: %w", err)
	}

	log.Info().
		Str("status", result.Status).
		Int("failing_streak", result.FailingStreak).
		Msg("Healthcheck completed")

	if len(result.Log) > 0 {
		lastLog := result.Log[len(result.Log)-1]
		log.Info().Str("output", lastLog.Output).Msg("Latest healthcheck output")
	}

	return nil
}

func listContainers(socket string) error {
	ctx := context.Background()

	// Connect to Podman
	conn, err := bindings.NewConnection(ctx, fmt.Sprintf("unix://%s", socket))
	if err != nil {
		return fmt.Errorf("failed to connect to Podman socket: %w", err)
	}

	log.Info().Msg("Discovering containers with healthchecks")

	// List all containers
	listOpts := &containers.ListOptions{All: boolPtr(true)}
	containerList, err := containers.List(conn, listOpts)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	// Filter and display containers with healthchecks
	found := false
	for _, ctr := range containerList {
		// Inspect to get healthcheck config
		inspectData, err := containers.Inspect(conn, ctr.ID, nil)
		if err != nil {
			log.Warn().Err(err).Str("container", ctr.ID).Msg("Failed to inspect container")
			continue
		}

		if inspectData.Config.Healthcheck != nil && len(inspectData.Config.Healthcheck.Test) > 0 {
			found = true

			interval := time.Duration(inspectData.Config.Healthcheck.Interval)
			timeout := time.Duration(inspectData.Config.Healthcheck.Timeout)

			healthStatus := "unknown"
			if inspectData.State != nil && inspectData.State.Health != nil {
				healthStatus = inspectData.State.Health.Status
			}

			log.Info().
				Str("id", ctr.ID[:12]).
				Str("name", ctr.Names[0]).
				Str("state", ctr.State).
				Str("health", healthStatus).
				Dur("interval", interval).
				Dur("timeout", timeout).
				Int("retries", inspectData.Config.Healthcheck.Retries).
				Msg("Container with healthcheck")
		}
	}

	if !found {
		log.Info().Msg("No containers with healthchecks found")
	}

	return nil
}

func boolPtr(b bool) *bool {
	return &b
}
