package main

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// Log levels, lowest first.
const (
	levelDebug = iota
	levelInfo
	levelWarn
	levelError
)

var levelNames = map[int]string{
	levelDebug: "debug", levelInfo: "info", levelWarn: "warn", levelError: "error",
}

var logLevel = levelInfo

func setLogLevel(name string) {
	switch name {
	case "debug":
		logLevel = levelDebug
	case "warn":
		logLevel = levelWarn
	case "error":
		logLevel = levelError
	default:
		logLevel = levelInfo
	}
}

var (
	logMu sync.Mutex
	// logOut is a variable so tests can capture output; production always
	// writes to stdout.
	logOut io.Writer = os.Stdout
)

// logEvent writes one JSON object per line to stdout. Request and response
// bodies are never passed here, at any level.
func logEvent(level int, event string, fields map[string]any) {
	if level < logLevel {
		return
	}
	rec := make(map[string]any, len(fields)+3)
	for k, v := range fields {
		rec[k] = v
	}
	rec["ts"] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	rec["level"] = levelNames[level]
	rec["event"] = event

	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	logOut.Write(append(b, '\n'))
}

func logInfo(event string, f map[string]any)  { logEvent(levelInfo, event, f) }
func logWarn(event string, f map[string]any)  { logEvent(levelWarn, event, f) }
func logError(event string, f map[string]any) { logEvent(levelError, event, f) }
func logDebug(event string, f map[string]any) { logEvent(levelDebug, event, f) }

// ---------------------------------------------------------------------------
// Tool call audit
// ---------------------------------------------------------------------------

// AuditRecord is one line per tool call, successful or not.
//
// It records what was touched, never what was written. A second brain holds
// the operator's private notes; an audit log that quotes them would be a
// second copy of the vault in the container's stdout.
type AuditRecord struct {
	User       string
	ClientID   string
	Tool       string
	Vault      string
	Path       string
	Paths      int
	Bytes      int
	DryRun     bool
	Truncated  bool
	DurationMS int64
	Error      string
}

func (a AuditRecord) emit() {
	f := map[string]any{
		"tool":        a.Tool,
		"user":        a.User,
		"duration_ms": a.DurationMS,
	}
	if a.Vault != "" {
		f["vault"] = a.Vault
	}
	if a.ClientID != "" {
		f["client_id"] = a.ClientID
	}
	if a.Path != "" {
		f["path"] = a.Path
	}
	if a.Paths > 0 {
		f["paths"] = a.Paths
	}
	if a.DryRun {
		f["dry_run"] = true
	}
	if a.Error != "" {
		f["error"] = a.Error
		logInfo("tool_call", f)
		return
	}
	f["bytes"] = a.Bytes
	if a.Truncated {
		f["truncated"] = true
	}
	logInfo("tool_call", f)
}
