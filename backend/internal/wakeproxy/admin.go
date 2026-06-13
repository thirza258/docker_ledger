package wakeproxy

import (
	"encoding/json"
	"net/http"
)

type AdminServer struct {
	manager *Manager
}

func NewAdminServer(mgr *Manager) *AdminServer {
	return &AdminServer{manager: mgr}
}

func (a *AdminServer) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/services", a.listServices)
	mux.HandleFunc("POST /admin/services", a.createService)
	mux.HandleFunc("POST /admin/services/{name}/activate", a.activate)
	mux.HandleFunc("POST /admin/services/{name}/deactivate", a.deactivate)
}

// RegisterMainRoutes registers wakeproxy admin endpoints on the main API
// server under /wakeproxy/… so the frontend can reach them through the
// Vite dev‑server proxy (/api/wakeproxy/… → /wakeproxy/…).
func (a *AdminServer) RegisterMainRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /wakeproxy/services", a.listServices)
	mux.HandleFunc("POST /wakeproxy/services", a.createService)
	mux.HandleFunc("POST /wakeproxy/services/{name}/activate", a.activate)
	mux.HandleFunc("POST /wakeproxy/services/{name}/deactivate", a.deactivate)
}

func (a *AdminServer) listServices(w http.ResponseWriter, r *http.Request) {
	a.manager.mu.RLock()
	defer a.manager.mu.RUnlock()
	resp := make([]map[string]any, 0, len(a.manager.services))
	for _, svc := range a.manager.services {
		resp = append(resp, map[string]any{
			"name":      svc.Config.Name,
			"host":      svc.Config.Host,
			"container": svc.Config.Container,
			"port":      svc.Config.Port,
			"network":   svc.Config.Network,
			"active":    svc.IsActive(),
			"state":     svc.state,
		})
	}
	json.NewEncoder(w).Encode(resp)
}

func (a *AdminServer) createService(w http.ResponseWriter, r *http.Request) {
	var cfg ServiceConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := a.manager.AddService(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "created",
		"service": cfg,
	})
}

func (a *AdminServer) activate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc, ok := a.manager.services[name]
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	svc.SetActive(true)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "activated"})
}

func (a *AdminServer) deactivate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc, ok := a.manager.services[name]
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	svc.SetActive(false)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "deactivated"})
}