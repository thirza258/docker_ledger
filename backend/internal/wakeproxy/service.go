package wakeproxy

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"
)

type ServiceState int

const (
	StateStopped ServiceState = iota
	StateStarting
	StateRunning
)

type ManagedService struct {
	Config  ServiceConfig
	manager *Manager

	mu    sync.Mutex
	state ServiceState
	cond  *sync.Cond // for waking up waiting requests

	containerID string
	targetURL   *url.URL

	idleTimer *time.Timer
	active    bool
	activeMu  sync.RWMutex
}

func NewManagedService(cfg ServiceConfig, mgr *Manager) *ManagedService {
	s := &ManagedService{
		Config:  cfg,
		manager: mgr,
		state:   StateStopped,
		active:  true,
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// IsActive returns whether auto‑start is allowed.
func (s *ManagedService) IsActive() bool {
	s.activeMu.RLock()
	defer s.activeMu.RUnlock()
	return s.active
}

// SetActive toggles activation. If deactivated and running, stops the container.
func (s *ManagedService) SetActive(active bool) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	s.active = active
	if !active && s.state == StateRunning {
		go s.manager.StopService(s)
	}
}

// AwaitReady blocks until the container is running.
// Multiple callers share the same startup process.
func (s *ManagedService) AwaitReady(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Already running?
	if s.state == StateRunning {
		return nil
	}

	// If stopped, start the container (only once)
	if s.state == StateStopped {
		if !s.IsActive() {
			return fmt.Errorf("service deactivated")
		}
		s.state = StateStarting
		go s.startupSequence()
	}

	// Wait for completion
	for s.state != StateRunning {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		s.cond.Wait()
	}
	return nil
}

// startupSequence starts the container and sets up the idle timer.
func (s *ManagedService) startupSequence() {
	s.mu.Lock()
	if s.state != StateStarting {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// 1. Start container via Docker
	containerID, err := s.manager.DockerStart(s.Config.Container)
	if err != nil {
		s.mu.Lock()
		s.state = StateStopped
		s.cond.Broadcast()
		s.mu.Unlock()
		return
	}
	s.containerID = containerID

	// 2. No health check – just wait a short moment for the container to be ready
	// (you can add a small fixed delay if needed, e.g. 2 seconds)
	time.Sleep(2 * time.Second)

	// 3. Get container IP and build target URL
	target, err := s.manager.GetContainerTarget(s)
	if err != nil {
		s.mu.Lock()
		s.state = StateStopped
		s.cond.Broadcast()
		s.mu.Unlock()
		_ = s.manager.DockerStop(containerID)
		return
	}
	s.targetURL = target

	// 4. Mark running
	s.mu.Lock()
	s.state = StateRunning
	s.cond.Broadcast()
	s.mu.Unlock()

	// 5. Start idle timer (3 hours)
	s.resetIdleTimer()
}

// resetIdleTimer stops the old timer and starts a new one.
func (s *ManagedService) resetIdleTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	if s.state != StateRunning {
		return
	}
	timeout := s.manager.idleTimeout
	if timeout == 0 {
		timeout = 3 * time.Hour
	}
	s.idleTimer = time.AfterFunc(timeout, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.state == StateRunning {
			s.state = StateStopped
			go func() {
				_ = s.manager.DockerStop(s.containerID)
			}()
		}
	})
}

// RecordActivity resets the idle timer (called after each request).
func (s *ManagedService) RecordActivity() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateRunning {
		s.resetIdleTimer()
	}
}

// TargetURL returns the proxy destination.
func (s *ManagedService) TargetURL() *url.URL {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targetURL
}