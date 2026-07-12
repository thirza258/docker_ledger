package wakeproxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
)

type ProxyHandler struct {
	manager *Manager
	proxies map[string]*httputil.ReverseProxy
	mu      sync.RWMutex
}

func NewProxyHandler(mgr *Manager) *ProxyHandler {
	return &ProxyHandler{
		manager: mgr,
		proxies: make(map[string]*httputil.ReverseProxy),
	}
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	svc, err := h.manager.GetServiceForHost(host)
	if err != nil {
		slog.Warn("wakeproxy: unknown service host", "host", host, "error", err)
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}

	// Check activation
	if !svc.IsActive() {
		slog.Info("wakeproxy: service deactivated", "service", svc.Config.Name, "host", host)
		http.Error(w, "service deactivated", http.StatusServiceUnavailable)
		return
	}

	// Await container start (may start it)
	if err := svc.AwaitReady(r.Context()); err != nil {
		slog.Error("wakeproxy: failed to start service", "service", svc.Config.Name, "host", host, "error", err)
		http.Error(w, "service starting or unavailable", http.StatusServiceUnavailable)
		return
	}

	// Get or create reverse proxy
	proxy := h.getProxy(svc)
	svc.RecordActivity() // reset idle timer
	proxy.ServeHTTP(w, r)
}

func (h *ProxyHandler) getProxy(svc *ManagedService) *httputil.ReverseProxy {
	h.mu.RLock()
	proxy, ok := h.proxies[svc.Config.Name]
	h.mu.RUnlock()
	if ok {
		return proxy
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if proxy, ok := h.proxies[svc.Config.Name]; ok {
		return proxy
	}
	target := svc.TargetURL()
	if target == nil {
		// fallback (should not happen)
		slog.Error("wakeproxy: nil target URL for service", "service", svc.Config.Name)
		target, _ = url.Parse("http://127.0.0.1:1")
	}
	proxy = httputil.NewSingleHostReverseProxy(target)
	h.proxies[svc.Config.Name] = proxy
	return proxy
}
