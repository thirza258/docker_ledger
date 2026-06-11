package wakeproxy

import (
	"context"
	"fmt"
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
	}
	return m, nil
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
		return "", fmt.Errorf("inspect: %w", err)
	}
	if insp.State.Running {
		return insp.ID, nil
	}
	
	if _, err := m.cli.ContainerStart(ctx, insp.ID, client.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("start: %w", err)
	}
	return insp.ID, nil
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

	return err
}

func (m *Manager) GetContainerTarget(svc *ManagedService) (*url.URL, error) {
	insp, err := m.cli.ContainerInspect(context.Background(), svc.containerID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	var ip string
	if svc.Config.Network != "" {
		netSettings, ok := insp.NetworkSettings.Networks[svc.Config.Network]
		if !ok {
			return nil, fmt.Errorf("network %s not found", svc.Config.Network)
		}
		ip = netSettings.IPAddress
	} else {
		ip = insp.NetworkSettings.IPAddress
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
	_ = m.DockerStop(svc.containerID)
}