package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/urfave/cli/v2"
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	// Setup context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Add logger to context
	ctx = log.Logger.WithContext(ctx)

	app := &cli.App{
		Name:  "pod-pulse",
		Usage: "Systemd-free healthcheck scheduler for Podman containers",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "socket",
				Aliases: []string{"s"},
				Value:   "/run/podman/podman.sock",
				Usage:   "Path to Podman socket",
				EnvVars: []string{"PODMAN_SOCKET"},
			},
			&cli.BoolFlag{
				Name:    "debug",
				Aliases: []string{"d"},
				Value:   false,
				Usage:   "Enable debug logging",
			},
		},
		Before: func(c *cli.Context) error {
			if c.Bool("debug") {
				zerolog.SetGlobalLevel(zerolog.DebugLevel)
			} else {
				zerolog.SetGlobalLevel(zerolog.InfoLevel)
			}
			return nil
		},
		Commands: []*cli.Command{
			{
				Name:  "daemon",
				Usage: "Run the healthcheck scheduler daemon",
				Action: func(c *cli.Context) error {
					return runDaemon(c.String("socket"), ctx)
				},
			},
			{
				Name:  "check",
				Usage: "Run a one-time healthcheck for a specific container",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "container",
						Aliases:  []string{"c"},
						Required: true,
						Usage:    "Container name or ID",
					},
				},
				Action: func(c *cli.Context) error {
					return runCheck(c.String("socket"), c.String("container"))
				},
			},
			{
				Name:  "list",
				Usage: "List all containers with healthchecks",
				Action: func(c *cli.Context) error {
					return listContainers(c.String("socket"))
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal().Err(err).Msg("Application error")
	}
}
