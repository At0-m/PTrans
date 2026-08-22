package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	listenAddr := envOrDefault("MOCK_WEBHOOK_ADDR", ":8090")
	defaultMode := envOrDefault("MOCK_WEBHOOK_MODE", "ok")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /hook", func(w http.ResponseWriter, r *http.Request) {
		mode := r.URL.Query().Get("mode")
		if mode == "" {
			mode = defaultMode
		}
		logger.Info("mock webhook received", "mode", mode, "event_id", r.Header.Get("X-PTrans-Event-ID"))

		switch mode {
		case "slow":
			time.Sleep(envOrDefaultDuration("MOCK_WEBHOOK_SLOW_DELAY", 6*time.Second))
		case "fail":
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "failed"})
			return
		case "random":
			if time.Now().UnixNano()%2 == 0 {
				writeJSON(w, http.StatusBadGateway, map[string]string{"status": "random failure"})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	logger.Info("mock webhook listening", "addr", listenAddr, "mode", defaultMode)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		logger.Error("mock webhook stopped", "error", err)
		os.Exit(1)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envOrDefaultDuration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		return time.Duration(seconds) * time.Second
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return value
}
