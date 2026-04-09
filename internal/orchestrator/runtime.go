// Package orchestrator manages Docker-based GCP emulators alongside
// localgcp's native Go services. It provides a lazy TCP proxy that binds
// ports instantly and only starts containers on first request.
package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// ContainerRuntime abstracts Docker operations for testability.
type ContainerRuntime interface {
	Available() bool
	Pull(ctx context.Context, img string) error
	Create(ctx context.Context, cfg ContainerConfig) (string, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	HostPort(ctx context.Context, id string, containerPort string) (string, error)
	FindExisting(ctx context.Context, name string) (string, bool, error)
	CleanupOrphans(ctx context.Context, prefix string) error
}

// ContainerConfig holds the parameters for creating a container.
type ContainerConfig struct {
	Name          string            // container name (e.g. "localgcp-spanner")
	Image         string            // Docker image (e.g. "gcr.io/cloud-spanner-emulator/emulator:1.5.23")
	InternalPort  string            // port inside container (e.g. "9010/tcp")
	Cmd           []string          // optional command override
	Env           []string          // environment variables
}

// DockerRuntime implements ContainerRuntime using the Docker Go SDK.
type DockerRuntime struct {
	cli    *client.Client
	logger *log.Logger
	avail  bool
}

// NewDockerRuntime creates a Docker runtime. If Docker is unavailable, Available() returns false.
func NewDockerRuntime(logger *log.Logger) *DockerRuntime {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		logger.Printf("Docker SDK init failed: %v", err)
		return &DockerRuntime{logger: logger}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		logger.Printf("Docker not available: %v", err)
		return &DockerRuntime{cli: cli, logger: logger}
	}

	return &DockerRuntime{cli: cli, logger: logger, avail: true}
}

func (d *DockerRuntime) Available() bool { return d.avail }

func (d *DockerRuntime) Pull(ctx context.Context, img string) error {
	d.logger.Printf("Pulling %s (may take 30s on first run)...", img)
	reader, err := d.cli.ImagePull(ctx, img, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("image pull %s: %w", img, err)
	}
	defer reader.Close()
	// Drain the pull output to completion.
	io.Copy(io.Discard, reader)
	d.logger.Printf("Pulled %s", img)
	return nil
}

func (d *DockerRuntime) Create(ctx context.Context, cfg ContainerConfig) (string, error) {
	port, err := nat.NewPort("tcp", strings.TrimSuffix(cfg.InternalPort, "/tcp"))
	if err != nil {
		return "", fmt.Errorf("parse port %s: %w", cfg.InternalPort, err)
	}

	containerCfg := &container.Config{
		Image: cfg.Image,
		ExposedPorts: nat.PortSet{
			port: struct{}{},
		},
		Env: cfg.Env,
	}
	if len(cfg.Cmd) > 0 {
		containerCfg.Cmd = cfg.Cmd
	}

	hostCfg := &container.HostConfig{
		PortBindings: nat.PortMap{
			port: []nat.PortBinding{{
				HostIP:   "127.0.0.1",
				HostPort: "0", // let Docker assign a free port
			}},
		},
	}

	resp, err := d.cli.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, cfg.Name)
	if err != nil {
		return "", fmt.Errorf("container create %s: %w", cfg.Name, err)
	}
	return resp.ID, nil
}

func (d *DockerRuntime) Start(ctx context.Context, id string) error {
	return d.cli.ContainerStart(ctx, id, container.StartOptions{})
}

func (d *DockerRuntime) Stop(ctx context.Context, id string) error {
	timeout := 10
	return d.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
}

func (d *DockerRuntime) Remove(ctx context.Context, id string) error {
	return d.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

func (d *DockerRuntime) HostPort(ctx context.Context, id string, containerPort string) (string, error) {
	info, err := d.cli.ContainerInspect(ctx, id)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", id, err)
	}

	port, _ := nat.NewPort("tcp", strings.TrimSuffix(containerPort, "/tcp"))
	bindings, ok := info.NetworkSettings.Ports[port]
	if !ok || len(bindings) == 0 {
		return "", fmt.Errorf("no host port for %s", containerPort)
	}
	return net.JoinHostPort("127.0.0.1", bindings[0].HostPort), nil
}

func (d *DockerRuntime) FindExisting(ctx context.Context, name string) (string, bool, error) {
	f := filters.NewArgs(filters.Arg("name", "^/"+name+"$"))
	containers, err := d.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return "", false, err
	}
	if len(containers) == 0 {
		return "", false, nil
	}
	c := containers[0]
	running := strings.HasPrefix(c.State, "running")
	return c.ID, running, nil
}

func (d *DockerRuntime) CleanupOrphans(ctx context.Context, prefix string) error {
	f := filters.NewArgs(filters.Arg("name", prefix))
	containers, err := d.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return err
	}
	for _, c := range containers {
		if c.State != "running" {
			d.logger.Printf("Removing orphaned container %s (%s)", c.Names[0], c.ID[:12])
			d.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
		}
	}
	return nil
}

// Suppress unused import warning.
var _ = os.Stderr
