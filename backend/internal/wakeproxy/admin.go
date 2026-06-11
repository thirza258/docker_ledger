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
	mux.HandleFunc("POST /admin/services/{name}/activate", a.activate)
	mux.HandleFunc("POST /admin/services/{name}/deactivate", a.deactivate)
}

func (a *AdminServer) listServices(w http.ResponseWriter, r *http.Request) {
	a.manager.mu.RLock()
	defer a.manager.mu.RUnlock()
	var resp []map[string]interface{}
	for name, svc := range a.manager.services {
		resp = append(resp, map[string]interface{}{
			"name":   name,
			"active": svc.IsActive(),
			"state":  svc.state,
			"host":   svc.Config.Host,
		})
	}
	json.NewEncoder(w).Encode(resp)
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