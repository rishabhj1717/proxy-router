package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/yourorg/alb/internal/config"
	"github.com/yourorg/alb/internal/proxy"
	"github.com/yourorg/alb/internal/router"
	"github.com/yourorg/alb/internal/store"
	"go.uber.org/zap"
)

func main() {
	// --- Logger -----------------------------------------------------------
	log, _ := zap.NewProduction()
	defer log.Sync()

	// --- Config -----------------------------------------------------------
	cfg := config.Load()
	log.Info("starting ALB",
		zap.String("listen", cfg.ListenAddr),
		zap.String("admin", cfg.AdminAddr),
		zap.String("db", cfg.DBPath),
	)

	// --- Persistence layer ------------------------------------------------
	s, err := store.New(cfg.DBPath)
	if err != nil {
		log.Fatal("failed to open store", zap.Error(err))
	}
	defer s.Close()

	// --- Routing engine ---------------------------------------------------
	engine, err := router.NewEngine(s, log)
	if err != nil {
		log.Fatal("failed to initialise routing engine", zap.Error(err))
	}

	// --- Proxy handler (data plane) ---------------------------------------
	proxyHandler := proxy.NewHandler(
		engine, log,
		time.Duration(cfg.DialTimeoutSec)*time.Second,
		time.Duration(cfg.ResponseTimeoutSec)*time.Second,
	)

	// --- Data plane server ------------------------------------------------
	dataPlane := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      proxyHandler,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// --- Admin API (control plane) ----------------------------------------
	adminMux := buildAdminMux(s, engine, log)
	adminPlane := &http.Server{
		Addr:         cfg.AdminAddr,
		Handler:      adminMux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	// --- Graceful shutdown ------------------------------------------------
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		log.Info("data plane listening", zap.String("addr", cfg.ListenAddr))
		if err := dataPlane.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("data plane error", zap.Error(err))
		}
	}()

	go func() {
		log.Info("admin plane listening", zap.String("addr", cfg.AdminAddr))
		if err := adminPlane.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("admin plane error", zap.Error(err))
		}
	}()

	<-quit
	log.Info("shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := dataPlane.Shutdown(ctx); err != nil {
		log.Error("data plane shutdown error", zap.Error(err))
	}
	if err := adminPlane.Shutdown(ctx); err != nil {
		log.Error("admin plane shutdown error", zap.Error(err))
	}

	log.Info("ALB stopped cleanly")
}

// ---------------------------------------------------------------------------
// Admin API — Control Plane
// ---------------------------------------------------------------------------

func buildAdminMux(s *store.Store, engine *router.Engine, log *zap.Logger) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("/routes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {

		case http.MethodGet:
			routes := engine.ListAll()
			json.NewEncoder(w).Encode(routes)

		case http.MethodPost:
			var req struct {
				SandboxID string `json:"sandbox_id"`
				Pattern   string `json:"pattern"`
				TargetURL string `json:"target_url"`
				Priority  int    `json:"priority"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
				return
			}
			if req.Pattern == "" || req.TargetURL == "" {
				http.Error(w, `{"error":"pattern and target_url are required"}`, http.StatusBadRequest)
				return
			}
			if req.Priority == 0 {
				req.Priority = 100
			}

			record := &store.Route{
				SandboxID: req.SandboxID,
				Pattern:   req.Pattern,
				TargetURL: req.TargetURL,
				Priority:  req.Priority,
			}
			id, err := s.Create(record)
			if err != nil {
				log.Error("store create route", zap.Error(err))
				http.Error(w, `{"error":"failed to persist route"}`, http.StatusInternalServerError)
				return
			}
			record.ID = id

			if err := engine.Add(*record); err != nil {
				s.Delete(id) // rollback the DB write
				http.Error(w, `{"error":"invalid regex pattern: `+err.Error()+`"}`, http.StatusBadRequest)
				return
			}

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": id})

		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/routes/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if err := engine.Reload(s); err != nil {
			http.Error(w, `{"error":"reload failed"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"reloaded"}`))
	})

	mux.HandleFunc("/routes/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		idStr := r.URL.Path[len("/routes/"):]
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id == 0 {
			http.Error(w, `{"error":"invalid route id"}`, http.StatusBadRequest)
			return
		}
		if err := s.Delete(id); err != nil {
			http.Error(w, `{"error":"db delete failed"}`, http.StatusInternalServerError)
			return
		}
		engine.Remove(id)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"deleted"}`))
	})

	mux.HandleFunc("/sandboxes/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		sandboxID := r.URL.Path[len("/sandboxes/"):]
		if sandboxID == "" {
			http.Error(w, `{"error":"sandbox_id required"}`, http.StatusBadRequest)
			return
		}
		n, err := s.DeleteBySandbox(sandboxID)
		if err != nil {
			http.Error(w, `{"error":"db delete failed"}`, http.StatusInternalServerError)
			return
		}
		engine.Reload(s)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"sandbox_id": sandboxID,
			"deleted":    n,
		})
	})

	return mux
}
