package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// The tool surface
//
// Thirty-four tools is a lot for an MCP server, and the reasoning is
// deliberate. A model picks a tool by reading its name and description, so
// tools with one clear purpose each are chosen correctly; a single tool with a
// mode parameter and conditional arguments is chosen incorrectly, and the
// failure shows up as a mangled note rather than an error.
//
// The list is also shaped by what goes wrong when an agent edits prose. Almost
// every tool here that looks like a convenience - note_outline, note_section_edit,
// note_related, dry_run - exists to avoid pulling a whole note into context and
// writing it back, because that round trip is where content silently vanishes.
// ---------------------------------------------------------------------------

type toolCtx struct {
	srv   *Server
	user  *User
	cfg   *Config
	vault *Vault
	args  map[string]any
}

type Tool struct {
	Name        string
	Title       string
	Description string
	Props       map[string]any
	Required    []string
	Mutates     bool
	// NeedsVault is false for the handful of tools that pick their own vault.
	NoVault bool
	Handler func(*toolCtx) (any, error)
}

var registry = map[string]*Tool{}
var registryOrder []string

func register(t *Tool) {
	registry[t.Name] = t
	registryOrder = append(registryOrder, t.Name)
}

// ---------------------------------------------------------------------------
// Schema helpers
// ---------------------------------------------------------------------------

func str(desc string) map[string]any   { return map[string]any{"type": "string", "description": desc} }
func boolp(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
func intp(desc string) map[string]any  { return map[string]any{"type": "integer", "description": desc} }
func strList(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
func enum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

const vaultDesc = "Vault to work in. Omit to use the default vault."

func (t *Tool) schema(defaultVault string) map[string]any {
	props := map[string]any{}
	for k, v := range t.Props {
		props[k] = v
	}
	if !t.NoVault {
		props["vault"] = str(vaultDesc + " Default: " + defaultVault + ".")
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             t.Required,
		"additionalProperties": false,
	}
}

// toolDefinitions returns what the client sees. A read-only user is not shown
// the mutating tools at all, rather than being shown them and refused: a tool
// that is offered and then denied wastes a round trip and confuses the model
// about what it is allowed to do.
func toolDefinitions(u *User) []map[string]any {
	dv := defaultVaultName
	out := make([]map[string]any, 0, len(registryOrder))
	for _, name := range registryOrder {
		t := registry[name]
		if t.Mutates && u != nil && u.ReadOnly {
			continue
		}
		d := map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.schema(dv),
		}
		if t.Title != "" {
			d["title"] = t.Title
		}
		if t.Mutates {
			d["annotations"] = map[string]any{"readOnlyHint": false, "destructiveHint": name == "note_delete"}
		} else {
			d["annotations"] = map[string]any{"readOnlyHint": true}
		}
		out = append(out, d)
	}
	return out
}

// ---------------------------------------------------------------------------
// Calling
// ---------------------------------------------------------------------------

type callParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) callTool(req rpcRequest, user *User, cfg *Config) *rpcResponse {
	var p callParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return rpcFail(req.ID, codeInvalidParams, "invalid tool call parameters")
	}
	t, ok := registry[p.Name]
	if !ok {
		return rpcFail(req.ID, codeMethodNotFound, "unknown tool: "+p.Name)
	}
	if p.Arguments == nil {
		p.Arguments = map[string]any{}
	}

	start := time.Now()
	rec := AuditRecord{User: user.Name, Tool: p.Name}

	if t.Mutates && user.ReadOnly {
		rec.Error = "read only"
		rec.DurationMS = time.Since(start).Milliseconds()
		rec.emit()
		return toolError(req.ID, errReadOnly.Error())
	}

	ctx := &toolCtx{srv: s, user: user, cfg: cfg, args: p.Arguments}
	if !t.NoVault {
		v, err := s.vaults.Get(user, ctx.optString("vault", ""))
		if err != nil {
			rec.Error = err.Error()
			rec.DurationMS = time.Since(start).Milliseconds()
			rec.emit()
			return toolError(req.ID, err.Error())
		}
		ctx.vault = v
		rec.Vault = v.Name
	}
	rec.Path = ctx.optString("path", "")
	rec.DryRun = ctx.optBool("dry_run", false)

	result, err := t.Handler(ctx)
	rec.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		rec.Error = err.Error()
		rec.emit()
		return toolError(req.ID, err.Error())
	}

	payload, truncated := encodeResult(result, cfg.MaxResponseBytes)
	rec.Bytes = len(payload)
	rec.Truncated = truncated
	rec.emit()

	res := map[string]any{
		"content": []map[string]any{{"type": "text", "text": payload}},
	}
	if m, ok := result.(map[string]any); ok {
		res["structuredContent"] = m
	}
	return rpcOK(req.ID, res)
}

// toolError reports a failure inside the result rather than as a JSON-RPC
// error, which is what the MCP specification asks for: the model should see
// the message and get a chance to correct itself.
func toolError(id json.RawMessage, msg string) *rpcResponse {
	return rpcOK(id, map[string]any{
		"isError": true,
		"content": []map[string]any{{"type": "text", "text": msg}},
	})
}

func encodeResult(v any, limit int) (string, bool) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("could not encode result: %v", err), false
	}
	if len(b) <= limit {
		return string(b), false
	}
	cut := string(b[:limit])
	return cut + fmt.Sprintf(
		"\n\n[truncated: the result was %d bytes, the limit is %d. Narrow the query, or use limit/offset.]",
		len(b), limit), true
}

// ---------------------------------------------------------------------------
// Argument access
// ---------------------------------------------------------------------------

func (c *toolCtx) optString(key, def string) string {
	if v, ok := c.args[key]; ok {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return def
}

// rawString keeps interior whitespace, which matters for content and for the
// exact-match strings note_edit works with.
func (c *toolCtx) rawString(key, def string) string {
	if v, ok := c.args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func (c *toolCtx) reqString(key string) (string, error) {
	s := c.rawString(key, "")
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return s, nil
}

func (c *toolCtx) optBool(key string, def bool) bool {
	if v, ok := c.args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func (c *toolCtx) optInt(key string, def int) int {
	if v, ok := c.args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		}
	}
	return def
}

func (c *toolCtx) optList(key string) []string {
	v, ok := c.args[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		return splitList(t)
	}
	return nil
}

func (c *toolCtx) optTime(key string) (time.Time, error) {
	s := c.optString(key, "")
	if s == "" {
		return time.Time{}, nil
	}
	return parseWhen(s)
}

// parseWhen accepts a date, a timestamp or a relative expression like "7d",
// because a model asking for "notes changed in the last week" should not have
// to compute a date first.
func parseWhen(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if strings.EqualFold(s, "today") {
		n := time.Now()
		return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, n.Location()), nil
	}
	if d, err := parseRelative(s); err == nil {
		return time.Now().Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02", "2006-01"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("could not read %q as a date: use YYYY-MM-DD, an RFC3339 timestamp, or a span such as 7d or 12h", s)
}

func parseRelative(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("not relative")
	}
	unit := s[len(s)-1]
	num := s[:len(s)-1]
	var mult time.Duration
	switch unit {
	case 'd':
		mult = 24 * time.Hour
	case 'w':
		mult = 7 * 24 * time.Hour
	case 'm':
		mult = 30 * 24 * time.Hour
	case 'y':
		mult = 365 * 24 * time.Hour
	case 'h':
		mult = time.Hour
	default:
		return 0, fmt.Errorf("not relative")
	}
	var n int
	if _, err := fmt.Sscanf(num, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("not relative")
	}
	return time.Duration(n) * mult, nil
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
