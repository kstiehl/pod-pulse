# pod-pulse 💗

A systemd-free healthcheck scheduler for Podman containers.

## Problem

Podman is daemonless, which means healthchecks normally require systemd timers to be executed. If you're running in a systemdless environment (CI/CD pipelines, Podman-in-Podman setups, minimal containers), healthchecks won't run automatically - you'd need to call `podman healthcheck run` manually.

## Solution

`pod-pulse` connects to your Podman socket and:
- Discovers containers with healthcheck configurations
- Schedules healthchecks based on the interval/timeout configured in the container
- Listens for container events (start/stop) and dynamically adds/removes containers
- Handles healthcheck start periods correctly
- Runs entirely in user space without requiring systemd

## Installation

```bash
go install github.com/kstiehl/pod-pulse@latest
```

Or build from source:

```bash
git clone https://github.com/kstiehl/pod-pulse.git
cd pod-pulse
go build
```

## Usage

### Run the daemon

```bash
# Use default socket (/run/podman/podman.sock)
pod-pulse daemon

# Specify custom socket
pod-pulse --socket /path/to/podman.sock daemon

# Enable debug logging
pod-pulse --debug daemon
```

The daemon will:
1. Discover all running containers with healthchecks
2. Start scheduling healthchecks based on their configured intervals
3. Listen for container start/stop events and adjust scheduling dynamically
4. Respect healthcheck start_period delays
5. Run until interrupted with Ctrl+C or SIGTERM

### List containers with healthchecks

```bash
pod-pulse list
```

### Run a one-time healthcheck

```bash
pod-pulse check --container <container-name-or-id>
```

## Environment Variables

- `PODMAN_SOCKET`: Path to Podman socket (default: `/run/podman/podman.sock`)

## Example Container

```bash
podman run -d \
  --name my-app \
  --health-cmd "curl -f http://localhost:8080/health || exit 1" \
  --health-interval 30s \
  --health-timeout 10s \
  --health-start-period 40s \
  --health-retries 3 \
  my-app-image
```

Then run `pod-pulse daemon` and it will automatically pick up this container and run healthchecks every 30 seconds.

## License

MIT
