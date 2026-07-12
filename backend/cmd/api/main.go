package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/thirzq/dockerledger/internal/collector"
	"github.com/thirzq/dockerledger/internal/config"
	"github.com/thirzq/dockerledger/internal/database"
	"github.com/thirzq/dockerledger/internal/docker"
	"github.com/thirzq/dockerledger/internal/handlers"
	"github.com/thirzq/dockerledger/internal/middleware"
	"github.com/thirzq/dockerledger/internal/services"
	"github.com/thirzq/dockerledger/internal/storage"
	"github.com/thirzq/dockerledger/internal/telemetry"
	"github.com/thirzq/dockerledger/internal/wakeproxy"
	"github.com/thirzq/dockerledger/internal/websocket"
)

func main() {
	if _, err := docker.GetClient(); err != nil {
		slog.Error("failed to initialize Docker client", "error", err)
		os.Exit(1)
	}

	cfg := config.Load()

	var proxyServer *http.Server
	if cfg.Wakeproxy != nil && cfg.Wakeproxy.ListenAddr != "" {
		mgr, err := wakeproxy.NewManager(cfg.Wakeproxy)
		if err != nil {
			slog.Error("failed to create wakeproxy manager", "error", err)
			os.Exit(1)
		}

		proxyHandler := wakeproxy.NewProxyHandler(mgr)

		proxyServer = &http.Server{
			Addr:    cfg.Wakeproxy.ListenAddr,
			Handler: proxyHandler,
		}
		go func() {
			slog.Info("WakeProxy listening", "addr", cfg.Wakeproxy.ListenAddr)
			if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("WakeProxy server failed", "error", err)
				os.Exit(1)
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
				slog.Info("WakeProxy admin listening", "addr", cfg.Wakeproxy.AdminAddr)
				if err := adminSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					slog.Error("WakeProxy admin server failed", "error", err)
					os.Exit(1)
				}
			}()
		}
	}

	gormDB, err := database.NewGormConnection(cfg)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
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
		telemetry.WithRequestID(r.Context()).Debug("route hit", "method", r.Method, "path", r.URL.Path, "upgrade", r.Header.Get("Upgrade"))

		if strings.HasSuffix(r.URL.Path, "/logs/live") {
			slog.Debug("ws route detected")
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

	// Single signal-driven shutdown via context
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go logCollector.Start(ctx)

	// Start main API server
	server := &http.Server{
		Addr:         "0.0.0.0:" + cfg.ServerPort,
		Handler:      middleware.RequestID(middleware.AccessLog(http.DefaultServeMux)),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("server starting", "port", cfg.ServerPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	slog.Info("shutting down gracefully...")

	// 1. Shutdown proxy server first
	if proxyServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := proxyServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("WakeProxy shutdown error", "error", err)
		}
	}

	// 2. Shutdown main HTTP server
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced shutdown", "error", err)
		os.Exit(1)
	}
	slog.Info("server exited properly")

	// 3. Shutdown log collector
	collectorShutdownCtx, collectorCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer collectorCancel()
	if err := logCollector.Shutdown(collectorShutdownCtx); err != nil {
		slog.Error("log collector shutdown error", "error", err)
	}

	// 4. Close Docker client
	if err := docker.Close(); err != nil {
		slog.Error("error closing Docker client", "error", err)
	}

	slog.Info("shutdown complete")
}
