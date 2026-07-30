package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

var (
	version = "0.1.0"
	commit  = "dev"
	built   = "unknown"
)

const defaultConfigPath = "/etc/secondbrain/config.yaml"

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

type Server struct {
	cfg      atomic.Pointer[Config]
	vaults   *VaultManager
	sessions *SessionStore
	mcp      *mcpSessions
	stop     chan struct{}

	loginLimiter    *KeyedLimiter
	registerLimiter *KeyedLimiter
	toolLimiter     *KeyedLimiter
}

func (s *Server) Config() *Config     { return s.cfg.Load() }
func (s *Server) setConfig(c *Config) { s.cfg.Store(c) }

func NewServer(cfg *Config) (*Server, error) {
	vm, err := NewVaultManager(cfg)
	if err != nil {
		return nil, err
	}
	s := &Server{
		vaults:   vm,
		sessions: NewSessionStore(),
		mcp:      newMCPSessions(),
		stop:     make(chan struct{}),
	}
	s.setConfig(cfg)
	s.loginLimiter = NewKeyedLimiter(cfg.LoginRateLimit)
	registerRate, _ := ParseRate("20/h")
	s.registerLimiter = NewKeyedLimiter(registerRate)
	toolRate, _ := ParseRate("600/m")
	s.toolLimiter = NewKeyedLimiter(toolRate)
	return s, nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "version": version, "vaults": len(s.vaults.List(nil)),
		})
	})
	mux.HandleFunc("/favicon.ico", serveFavicon)
	mux.HandleFunc("/favicon.png", serveFavicon)
	mux.HandleFunc("/logo.png", serveFavicon)
	mux.HandleFunc("/.well-known/oauth-protected-resource", s.handleProtectedResource)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", s.handleProtectedResource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.handleAuthServerMetadata)
	mux.HandleFunc("/.well-known/oauth-authorization-server/mcp", s.handleAuthServerMetadata)
	mux.HandleFunc("/register", s.handleRegister)
	mux.HandleFunc("/authorize", s.handleAuthorize)
	mux.HandleFunc("/token", s.handleToken)
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			writeHTTPError(w, http.StatusNotFound, "not found")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "secondbrain %s\nMCP endpoint: %s\n", version, s.Config().endpoint("/mcp"))
	})

	return securityWrapper(mux)
}

func securityWrapper(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "hashpw":
			os.Exit(cmdHashPassword())
		case "validate":
			os.Exit(cmdValidate(configPath(args)))
		case "reindex":
			os.Exit(cmdReindex(configPath(args)))
		case "version", "-v", "--version":
			fmt.Printf("secondbrain %s (commit %s, built %s)\n", version, commit, built)
			return
		case "help", "-h", "--help":
			usage()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
			usage()
			os.Exit(2)
		}
	}
	os.Exit(run())
}

func configPath(args []string) string {
	if len(args) > 1 {
		return args[1]
	}
	if p := os.Getenv("SECONDBRAIN_CONFIG"); p != "" {
		return p
	}
	return defaultConfigPath
}

func usage() {
	fmt.Print(`secondbrain - an MCP server that keeps your notes as plain Markdown

Usage:
  secondbrain                 Run the server
  secondbrain validate [path] Check the configuration and print what it means
  secondbrain reindex [path]  Rebuild the search index for every vault
  secondbrain hashpw          Read a password and print a bcrypt hash
  secondbrain version         Print version information

Environment:
  SECONDBRAIN_USERNAME       login name (required unless a config file defines users)
  SECONDBRAIN_PASSWORD       password, literal or bcrypt: or env: or file:
  SECONDBRAIN_PUBLIC_URL     external base URL, required
  SECONDBRAIN_DATA           vault root (default ` + defaultDataDir + `)
  SECONDBRAIN_DEFAULT_VAULT  vault used when a tool omits one (default ` + defaultVaultName + `)
  SECONDBRAIN_LISTEN         listen address (default ` + defaultListen + `)
  SECONDBRAIN_CONFIG         optional config file (default ` + defaultConfigPath + `)
  SECONDBRAIN_GIT            commit every change (default true)
  SECONDBRAIN_GIT_REMOTE     push to this remote after each commit
  SECONDBRAIN_GIT_TOKEN      token used for that push
  SECONDBRAIN_LOG_LEVEL      debug|info|warn|error
`)
}

func run() int {
	setLogLevel(os.Getenv("SECONDBRAIN_LOG_LEVEL"))

	cfg, err := LoadConfig(configPath(nil))
	if err != nil {
		logError("config_error", map[string]any{"error": err.Error()})
		return 1
	}

	s, err := NewServer(cfg)
	if err != nil {
		logError("startup_failed", map[string]any{"error": err.Error()})
		return 1
	}
	defer s.vaults.Close()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	names := make([]string, 0)
	for _, v := range s.vaults.List(nil) {
		names = append(names, v.Name)
		if err := v.idx.Reconcile(v); err != nil {
			logWarn("reconcile_failed", map[string]any{"vault": v.Name, "error": err.Error()})
		}
		if _, err := StartWatcher(v, s.stop); err != nil {
			logWarn("watch_unavailable", map[string]any{"vault": v.Name, "error": err.Error()})
		}
	}

	go s.sessions.RunJanitor(s.stop)
	go s.housekeeping()
	if cfg.Source != "environment" {
		go s.watchConfig(cfg.Source)
	}

	logInfo("startup", map[string]any{
		"version":    version,
		"listen":     cfg.Listen,
		"public_url": cfg.Issuer(),
		"users":      len(cfg.Users),
		"data_dir":   cfg.DataDir,
		"vaults":     names,
		"git":        cfg.Git,
		"config":     cfg.Source,
	})

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		logError("shutdown", map[string]any{"error": err.Error()})
		return 1
	case <-sig:
	}

	close(s.stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	logInfo("shutdown", map[string]any{"reason": "signal"})
	return 0
}

// housekeeping sweeps expired MCP sessions and old trash.
func (s *Server) housekeeping() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.mcp.sweep()
			retention := s.Config().TrashRetention
			for _, v := range s.vaults.List(nil) {
				if n := v.PurgeTrash(retention); n > 0 {
					logInfo("trash_purged", map[string]any{"vault": v.Name, "removed": n})
				}
			}
		case <-s.stop:
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Subcommands
// ---------------------------------------------------------------------------

func cmdHashPassword() int {
	var pw []byte
	var err error

	if term.IsTerminal(int(syscall.Stdin)) {
		fmt.Fprint(os.Stderr, "Password: ")
		pw, err = term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "could not read password")
			return 1
		}
		fmt.Fprint(os.Stderr, "Repeat:   ")
		again, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if err != nil || string(again) != string(pw) {
			fmt.Fprintln(os.Stderr, "passwords do not match")
			return 1
		}
	} else {
		buf := make([]byte, 0, 256)
		tmp := make([]byte, 256)
		for {
			n, rerr := os.Stdin.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if rerr != nil {
				break
			}
		}
		pw = []byte(strings.TrimRight(string(buf), "\r\n"))
	}

	if len(pw) == 0 {
		fmt.Fprintln(os.Stderr, "empty password")
		return 1
	}
	h, err := bcrypt.GenerateFromPassword(pw, 12)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hashing failed")
		return 1
	}
	fmt.Printf("bcrypt:%s\n", h)
	return 0
}

func cmdValidate(path string) int {
	cfg, err := LoadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration is not usable: %v\n", err)
		return 1
	}
	fmt.Printf("configuration OK (from %s)\n", cfg.Source)
	fmt.Printf("  public_url:    %s\n", cfg.Issuer())
	fmt.Printf("  listen:        %s\n", cfg.Listen)
	fmt.Printf("  data_dir:      %s\n", cfg.DataDir)
	fmt.Printf("  default_vault: %s\n", cfg.DefaultVault)
	fmt.Printf("  git:           %v\n", cfg.Git)
	if len(cfg.AllowedOrigins) > 0 {
		fmt.Printf("  origins:       %s\n", strings.Join(cfg.AllowedOrigins, ", "))
	}
	for _, name := range sortedUserNames(cfg) {
		u := cfg.Users[name]
		scope := "every vault"
		if len(u.Vaults) > 0 {
			scope = strings.Join(u.Vaults, ", ")
		}
		mode := "read/write"
		if u.ReadOnly {
			mode = "read only"
		}
		hashed := "plain password"
		if u.PasswordIsHash {
			hashed = "bcrypt hash"
		}
		fmt.Printf("\n  user %s\n    vaults: %s\n    access: %s\n    secret: %s\n", u.Name, scope, mode, hashed)
	}
	return 0
}

func cmdReindex(path string) int {
	cfg, err := LoadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	vm, err := NewVaultManager(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer vm.Close()
	for _, v := range vm.List(nil) {
		if err := v.idx.Reconcile(v); err != nil {
			fmt.Fprintf(os.Stderr, "vault %s: %v\n", v.Name, err)
			return 1
		}
		st, _ := v.idx.Stats()
		fmt.Printf("%-20s %5d notes  %6d words  %3d tags  %3d broken links\n",
			v.Name, st.Notes, st.Words, st.Tags, st.BrokenLinks)
	}
	return 0
}

func sortedUserNames(c *Config) []string {
	names := make([]string, 0, len(c.Users))
	for n := range c.Users {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
