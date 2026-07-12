package wakeproxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/moby/moby/client"
	"github.com/thirzq/dockerledger/internal/docker"
)

type Manager struct {
	cli         *client.Client // singleton, owned by docker package
	cfg         *Config
	idleTimeout time.Duration

	services  map[string]*ManagedService
	hostIndex map[string]string
	mu        sync.RWMutex
}

func NewManager(cfg *Config) (*Manager, error) {
	cli, err := docker.GetClient()
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	if err := docker.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("docker ping: %w", err)
	}

	m := &Manager{
		cli:         cli,
		cfg:         cfg,
		idleTimeout: cfg.IdleTimeout,
		services:    make(map[string]*ManagedService),
		hostIndex:   make(map[string]string),
	}
	for _, svcCfg := range cfg.Services {
		svc := NewManagedService(svcCfg, m)
		m.services[svcCfg.Name] = svc
		m.hostIndex[svcCfg.Host] = svcCfg.Name
		slog.Info("wakeproxy: registered service", "name", svcCfg.Name, "host", svcCfg.Host, "container", svcCfg.Container)
	}
	slog.Info("wakeproxy: manager initialized", "services", len(m.services))
	return m, nil
}

// AddService dynamically registers a new wakeproxy service.
// Only host and container are required; port defaults to 80 and
// name is derived from the host if left empty.
func (m *Manager) AddService(cfg ServiceConfig) error {
	if cfg.Host == "" {
		return fmt.Errorf("service host is required")
	}
	if cfg.Container == "" {
		return fmt.Errorf("service container is required")
	}
	if cfg.Port <= 0 {
		cfg.Port = 80
	}
	if cfg.Name == "" {
		cfg.Name = cfg.Host
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.services[cfg.Name]; exists {
		return fmt.Errorf("service %q already exists", cfg.Name)
	}
	if _, exists := m.hostIndex[cfg.Host]; exists {
		return fmt.Errorf("host %q already registered", cfg.Host)
	}

	svc := NewManagedService(cfg, m)
	m.services[cfg.Name] = svc
	m.hostIndex[cfg.Host] = cfg.Name
	slog.Info("wakeproxy: dynamically added service", "name", cfg.Name, "host", cfg.Host, "container", cfg.Container)
	return nil
}

func (m *Manager) GetServiceForHost(host string) (*ManagedService, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	name, ok := m.hostIndex[host]
	if !ok {
		return nil, fmt.Errorf("no service for host %s", host)
	}
	return m.services[name], nil
}

func (m *Manager) DockerStart(containerName string) (string, error) {
	ctx := context.Background()
	insp, err := m.cli.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{})
	if err != nil {
		slog.Error("wakeproxy: container inspect failed", "container", containerName, "error", err)
		return "", fmt.Errorf("inspect: %w", err)
	}
	if insp.Container.State.Running {
		return insp.Container.ID, nil
	}

	if _, err := m.cli.ContainerStart(ctx, insp.Container.ID, client.ContainerStartOptions{}); err != nil {
		slog.Error("wakeproxy: container start failed", "container", containerName, "error", err)
		return "", fmt.Errorf("start: %w", err)
	}
	slog.Info("wakeproxy: container started", "container", containerName, "id", insp.Container.ID)
	return insp.Container.ID, nil
}

func (m *Manager) DockerStop(containerID string) error {
	timeout := 10

	_, err := m.cli.ContainerStop(
		context.Background(),
		containerID,
		client.ContainerStopOptions{
			Timeout: &timeout,
		},
	)
	if err != nil {
		slog.Error("wakeproxy: container stop failed", "container_id", containerID, "error", err)
	}
	return err
}

func (m *Manager) GetContainerTarget(svc *ManagedService) (*url.URL, error) {
	insp, err := m.cli.ContainerInspect(
		context.Background(),
		svc.containerID,
		client.ContainerInspectOptions{},
	)
	if err != nil {
		slog.Error("wakeproxy: container target inspect failed", "container_id", svc.containerID, "service", svc.Config.Name, "error", err)
		return nil, err
	}

	var ip string

	if svc.Config.Network != "" {
		netSettings, ok := insp.Container.NetworkSettings.Networks[svc.Config.Network]
		if !ok {
			return nil, fmt.Errorf("network %s not found", svc.Config.Network)
		}
		ip = netSettings.IPAddress.String()
	} else {
		// Use the first network attached to the container
		for _, netSettings := range insp.Container.NetworkSettings.Networks {
			ip = netSettings.IPAddress.String()
			if ip != "" {
				break
			}
		}
	}

	if ip == "" {
		return nil, fmt.Errorf("no IP address for container")
	}

	return url.Parse(fmt.Sprintf("http://%s:%d", ip, svc.Config.Port))
}

func (m *Manager) StopService(svc *ManagedService) {
	if svc.containerID == "" {
		return
	}
	slog.Info("wakeproxy: stopping service due to idle timeout", "service", svc.Config.Name, "container_id", svc.containerID)
	_ = m.DockerStop(svc.containerID)
}
