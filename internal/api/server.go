// Package api is hearthd's HTTP and WebSocket front end. It only ever
// listens on 127.0.0.1 (SECURITY S1) and applies the S3–S5 checks to every
// request before any handler runs.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/auth"
)

// Config configures a Server.
type Config struct {
	// Port on 127.0.0.1; 0 picks a free port (tests).
	Port int
	Auth *auth.Manager
	// Static serves the embedded UI at /. Nil serves 404 (until ROADMAP 0.7).
	Static fs.FS
	Logger *slog.Logger
}

// Server owns the listener, routes and WebSocket hub.
type Server struct {
	cfg  Config
	port atomic.Int64
	log  *slog.Logger

	// API is the authenticated mux for /api/v1 routes, e.g.
	// s.API.HandleFunc("GET /api/v1/containers", ...).
	API *http.ServeMux
	Hub *Hub

	srv *http.Server
	ln  net.Listener
}

// New builds a Server; it does not listen until Listen is called.
func New(cfg Config) (*Server, error) {
	if cfg.Auth == nil {
		return nil, errors.New("api: Config.Auth is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{cfg: cfg, log: cfg.Logger, API: http.NewServeMux()}
	s.Hub = newHub(s.log)
	s.srv = &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// No WriteTimeout: it would cut long-lived WebSockets. Handlers
		// bound their own work with contexts.
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 64 << 10,
		ErrorLog:       slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	return s, nil
}

func (s *Server) handler() http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("GET /auth", s.cfg.Auth.HandleAuth)
	root.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	root.Handle("GET /api/v1/ws", s.cfg.Auth.Require(http.HandlerFunc(s.Hub.serveWS)))
	root.Handle("/api/", s.cfg.Auth.Require(s.API))
	// "/" rather than "GET /": the latter conflicts with "/api/" in the mux.
	root.Handle("/", staticHandler(s.cfg.Static))

	port := func() int { return int(s.port.Load()) }
	var h http.Handler = root
	h = rateLimit(newBucket(20, 40), h)
	h = bodyLimit(h)
	h = originCheck(port, h)
	h = hostCheck(port, h)
	h = securityHeaders(h)
	return h
}

// Listen binds 127.0.0.1:<port>. Only loopback is possible: the address is
// not configurable.
func (s *Server) Listen(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp4", fmt.Sprintf("127.0.0.1:%d", s.cfg.Port))
	if err != nil {
		return fmt.Errorf("api: listen: %w", err)
	}
	s.ln = ln
	s.port.Store(int64(ln.Addr().(*net.TCPAddr).Port))
	return nil
}

// Port returns the bound port (after Listen).
func (s *Server) Port() int { return int(s.port.Load()) }

// Addr returns the listener address (after Listen).
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Serve runs until Shutdown; it returns nil after a clean shutdown.
func (s *Server) Serve() error {
	if s.ln == nil {
		return errors.New("api: Serve before Listen")
	}
	s.log.Info("listening", "addr", s.ln.Addr().String())
	if err := s.srv.Serve(s.ln); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("api: serve: %w", err)
	}
	return nil
}

// Shutdown stops accepting requests, closes WebSockets and waits for
// in-flight handlers until ctx expires.
func (s *Server) Shutdown(ctx context.Context) error {
	s.Hub.closeAll()
	return s.srv.Shutdown(ctx)
}

func staticHandler(static fs.FS) http.Handler {
	files := http.NotFoundHandler()
	if static != nil {
		files = http.FileServerFS(static)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSafeMethod(r.Method) {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
