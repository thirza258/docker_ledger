package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"context"
	"time"
	"strings"

	"github.com/thirzq/dockerledger/internal/docker"
	"github.com/thirzq/dockerledger/internal/handlers"
	"github.com/thirzq/dockerledger/internal/services"
	"github.com/thirzq/dockerledger/internal/websocket"
	"github.com/thirzq/dockerledger/internal/config"
	"github.com/thirzq/dockerledger/internal/database"
	"github.com/thirzq/dockerledger/internal/storage"
	"github.com/thirzq/dockerledger/internal/collector"
	"github.com/thirzq/dockerledger/internal/wakeproxy"
	
)

func main() {
	if _, err := docker.GetClient(); err != nil {
		log.Fatalf("Failed to initialize Docker client: %v", err)
	}

	cfg := config.Load()

	var proxyServer *http.Server
    if cfg.Wakeproxy != nil && cfg.Wakeproxy.ListenAddr != "" {
        mgr, err := wakeproxy.NewManager(cfg.Wakeproxy)
        if err != nil {
            log.Fatalf("Failed to create wakeproxy manager: %v", err)
        }

        proxyHandler := wakeproxy.NewProxyHandler(mgr)

        proxyServer = &http.Server{
            Addr:    cfg.Wakeproxy.ListenAddr,
            Handler: proxyHandler,
        }
        go func() {
            log.Printf("WakeProxy listening on %s", cfg.Wakeproxy.ListenAddr)
            if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
                log.Fatalf("WakeProxy server failed: %v", err)
            }
        }()

        if cfg.Wakeproxy.AdminEnabled && cfg.Wakeproxy.AdminAddr != "" {
            adminMux := http.NewServeMux()
            adminServer := wakeproxy.NewAdminServer(mgr)
            adminServer.RegisterHandlers(adminMux)

            // Also register wakeproxy admin routes on the main API server
            // so the frontend can reach them via /api/wakeproxy/…
            adminServer.RegisterMainRoutes(http.DefaultServeMux)

            adminSrv := &http.Server{
                Addr:    cfg.Wakeproxy.AdminAddr,
                Handler: adminMux,
            }
            go func() {
                log.Printf("WakeProxy admin listening on %s", cfg.Wakeproxy.AdminAddr)
                if err := adminSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
                    log.Fatalf("WakeProxy admin server failed: %v", err)
                }
            }()
        }
    }

	gormDB, err := database.NewGormConnection(cfg)
    if err != nil {
        log.Fatalf("Failed to connect to database: %v", err)
    }

	containerService := services.NewContainerService()
	logRepo := storage.NewLogRepository(gormDB)
	

	containerHandler := handlers.NewContainerHandler(containerService, logRepo)
	wsHandler := websocket.NewLogStreamHandler(containerService)
	multiLogHandler := websocket.NewMultiLogStreamHandler(containerService)
	aiSummaryService := services.NewAISummaryService(cfg, logRepo)
	aiSummaryHandler := handlers.NewAISummaryHandler(aiSummaryService)

	containerRepo := storage.NewContainerRepository(gormDB)
	_ = containerRepo

	http.HandleFunc("/logs/live", multiLogHandler.ServeHTTP)
	http.HandleFunc("/health/docker", containerHandler.DockerHealthCheck)
	http.HandleFunc("/containers", containerHandler.ListContainers)
    http.HandleFunc("/containers/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf(
        "ROUTE HIT method=%s path=%s upgrade=%s",
        r.Method,
        r.URL.Path,
        r.Header.Get("Upgrade"),
    )

    if strings.HasSuffix(r.URL.Path, "/logs/live") {
        log.Printf("WS ROUTE DETECTED")
        wsHandler.ServeHTTP(w, r)
        return
    } else if strings.HasSuffix(r.URL.Path, "/logs") {
            containerHandler.GetContainerLogs(w, r)
        } else if strings.HasSuffix(r.URL.Path, "/stats") { 
            containerHandler.GetContainerStats(w, r)
        } else if strings.Count(r.URL.Path, "/") == 2 && strings.HasPrefix(r.URL.Path, "/containers/") && !strings.Contains(r.URL.Path, "/logs") {
            containerHandler.GetContainer(w, r)
        } else if r.URL.Path == "/containers" {
            containerHandler.ListContainers(w, r)
        } else {
            http.NotFound(w, r)
        }
    })
	http.HandleFunc("/logs/search", containerHandler.SearchLogs)
	http.HandleFunc("/logs/summarize", aiSummaryHandler.GenerateSummary)
	http.HandleFunc("/logs/summarize/container", aiSummaryHandler.GenerateContainerSummary)

	logCollector := collector.NewLogCollector(containerService, containerRepo, logRepo)

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()
    go logCollector.Start(ctx)

	// Start server
	server := &http.Server{
		Addr:         "0.0.0.0:8080",
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Println("Server starting on :8080")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-ctx.Done()
    log.Println("Shutting down gracefully...")

	if proxyServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := proxyServer.Shutdown(ctx); err != nil {
			log.Printf("WakeProxy shutdown error: %v", err)
		}
	}
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	if err := docker.Close(); err != nil {
		log.Printf("Error closing Docker client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced shutdown: %v", err)
	}
	log.Println("Server exited properly")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    if err := logCollector.Shutdown(shutdownCtx); err != nil {
        log.Printf("Log collector shutdown error: %v", err)
    }

}