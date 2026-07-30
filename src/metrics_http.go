package main

import (
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// The metrics endpoint
//
// Off by default. A knowledge base is not a public service, and an endpoint
// that reports how many notes somebody has and when they last wrote one is not
// something to expose without being asked.
//
// When it is on there are two ways to keep it private, and they compose:
// a shared key, and a separate listener that you simply do not publish. The
// second is the stronger of the two - a port bound to the Docker network and
// never mapped cannot be reached from outside regardless of what the key is.
// ---------------------------------------------------------------------------

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config()
	if !cfg.Metrics {
		writeHTTPError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Its own rate limit bucket, so a scraper with the wrong key cannot be
	// used as a timing oracle and cannot exhaust the login limiter.
	if ok, _ := s.metricsLimiter.Allow(clientIP(r)); !ok {
		writeHTTPError(w, http.StatusTooManyRequests, "too many requests")
		return
	}

	// A key is optional, because on a private listener it buys nothing. When
	// one is set it is enforced strictly.
	if cfg.MetricsKey != "" && !metricsKeyOK(r, cfg.MetricsKey) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="secondbrain metrics"`)
		writeHTTPError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	body := s.metrics.Render(s.metricsSnapshot(cfg))

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write([]byte(body))
}

// metricsKeyOK accepts the key as a bearer token, or as X-API-Key for scrapers
// that find that easier. Both comparisons are constant time.
func metricsKeyOK(r *http.Request, key string) bool {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		if constantTimeEqual(strings.TrimSpace(auth[len(prefix):]), key) {
			return true
		}
	}
	if v := r.Header.Get("X-API-Key"); v != "" {
		return constantTimeEqual(v, key)
	}
	return false
}

// metricsSnapshot gathers what the registry cannot know by itself. Vault
// statistics come from the index, which means a scrape is a handful of counting
// queries against SQLite rather than a walk of the filesystem.
func (s *Server) metricsSnapshot(cfg *Config) snapshot {
	tokens, refresh, clients := s.sessions.Counts()

	snap := snapshot{
		Version: version, Commit: commit,
		Users: len(cfg.Users), Tokens: tokens, Refresh: refresh,
		Clients: clients, Sessions: s.mcp.count(),
	}
	for _, v := range s.vaults.List(nil) {
		st, err := v.idx.Stats()
		if err != nil {
			continue
		}
		snap.Vaults = append(snap.Vaults, vaultSnapshot{Name: v.Name, Stats: st})
	}
	return snap
}

// startMetricsListener brings up the second HTTP server when metrics are
// configured on their own address.
func (s *Server) startMetricsListener(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc(s.Config().MetricsPath, s.handleMetrics)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv := &http.Server{Addr: addr, Handler: securityWrapper(mux)}
	go func() {
		<-s.stop
		_ = srv.Close()
	}()
	logInfo("metrics_listener", map[string]any{"listen": addr, "path": s.Config().MetricsPath})
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logError("metrics_listener_failed", map[string]any{"error": err.Error()})
	}
}
