package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Metrics
//
// A hand-rolled registry rather than the Prometheus client library, for the
// same reason the rest of this program has almost no dependencies: the
// exposition format is a dozen lines of text, and pulling in a library plus
// its transitive tree to produce it is a poor trade in a container that holds
// somebody's private notes.
//
// Everything here is a count or a gauge over things the server already knows.
// No metric carries a note path, a title, a tag or a user's content - the
// label set is deliberately low cardinality, which is both a privacy property
// and the difference between a time series database that works and one that
// falls over.
// ---------------------------------------------------------------------------

type Metrics struct {
	mu    sync.Mutex
	start time.Time

	toolCalls    map[string]int64 // tool|outcome
	toolDuration map[string]float64
	toolBytes    map[string]int64

	httpRequests map[string]int64 // path|status
	logins       map[string]int64 // outcome

	indexUpdates    int64
	indexReconciles int64
	indexErrors     int64
	watchEvents     int64
	gitCommits      int64
	gitFailures     int64
	trashPurged     int64
	writes          int64
	dryRuns         int64
	truncated       int64
}

func NewMetrics() *Metrics {
	return &Metrics{
		start:        time.Now(),
		toolCalls:    map[string]int64{},
		toolDuration: map[string]float64{},
		toolBytes:    map[string]int64{},
		httpRequests: map[string]int64{},
		logins:       map[string]int64{},
	}
}

// ObserveTool records one tool call. Called from the audit path, so a metric
// can never disagree with the log.
func (m *Metrics) ObserveTool(tool, outcome string, seconds float64, bytes int, dryRun, truncated, mutating bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toolCalls[tool+"|"+outcome]++
	m.toolDuration[tool] += seconds
	m.toolBytes[tool] += int64(bytes)
	if dryRun {
		m.dryRuns++
	}
	if truncated {
		m.truncated++
	}
	if mutating && outcome == "ok" && !dryRun {
		m.writes++
	}
}

func (m *Metrics) ObserveHTTP(path string, status int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.httpRequests[path+"|"+fmt.Sprint(status)]++
}

func (m *Metrics) ObserveLogin(outcome string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logins[outcome]++
}

// add takes the field by accessor rather than by pointer so that a nil
// receiver is safe. Passing &m.field would dereference nil before the method
// body ever ran, which is a crash in code whose entire job is bookkeeping.
func (m *Metrics) add(pick func(*Metrics) *int64, n int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	*pick(m) += n
}

func (m *Metrics) IndexUpdated() {
	m.add(func(x *Metrics) *int64 { return &x.indexUpdates }, 1)
}
func (m *Metrics) IndexReconciled() {
	m.add(func(x *Metrics) *int64 { return &x.indexReconciles }, 1)
}
func (m *Metrics) IndexError() {
	m.add(func(x *Metrics) *int64 { return &x.indexErrors }, 1)
}
func (m *Metrics) WatchEvents(n int) {
	m.add(func(x *Metrics) *int64 { return &x.watchEvents }, int64(n))
}
func (m *Metrics) GitCommit() {
	m.add(func(x *Metrics) *int64 { return &x.gitCommits }, 1)
}
func (m *Metrics) GitFailure() {
	m.add(func(x *Metrics) *int64 { return &x.gitFailures }, 1)
}
func (m *Metrics) TrashPurged(n int) {
	m.add(func(x *Metrics) *int64 { return &x.trashPurged }, int64(n))
}

// snapshot is the part of the picture the registry cannot know: it has to be
// gathered from the session store and the vaults at scrape time.
type snapshot struct {
	Version  string
	Commit   string
	Users    int
	Tokens   int
	Refresh  int
	Clients  int
	Sessions int
	Vaults   []vaultSnapshot
}

type vaultSnapshot struct {
	Name  string
	Stats *VaultStats
}

func (m *Metrics) Render(s snapshot) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder
	write := func(name, help, typ string, lines ...string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}

	write("secondbrain_build_info", "Build information as labels, always 1.", "gauge",
		fmt.Sprintf(`secondbrain_build_info{version=%q,commit=%q} 1`, s.Version, s.Commit))
	write("secondbrain_uptime_seconds", "Seconds since the process started.", "gauge",
		fmt.Sprintf("secondbrain_uptime_seconds %.0f", time.Since(m.start).Seconds()))

	write("secondbrain_users", "Configured users.", "gauge",
		fmt.Sprintf("secondbrain_users %d", s.Users))
	write("secondbrain_oauth_clients", "OAuth clients registered since start. In memory only.", "gauge",
		fmt.Sprintf("secondbrain_oauth_clients %d", s.Clients))
	write("secondbrain_access_tokens", "Live access tokens.", "gauge",
		fmt.Sprintf("secondbrain_access_tokens %d", s.Tokens))
	write("secondbrain_refresh_tokens", "Live refresh tokens.", "gauge",
		fmt.Sprintf("secondbrain_refresh_tokens %d", s.Refresh))
	write("secondbrain_mcp_sessions", "Open MCP sessions.", "gauge",
		fmt.Sprintf("secondbrain_mcp_sessions %d", s.Sessions))

	var notes, words, bytes, tags, links, broken, orphans, tasks, atts []string
	for _, v := range s.Vaults {
		l := fmt.Sprintf("{vault=%q}", v.Name)
		notes = append(notes, fmt.Sprintf("secondbrain_vault_notes%s %d", l, v.Stats.Notes))
		words = append(words, fmt.Sprintf("secondbrain_vault_words%s %d", l, v.Stats.Words))
		bytes = append(bytes, fmt.Sprintf("secondbrain_vault_bytes%s %d", l, v.Stats.Bytes))
		tags = append(tags, fmt.Sprintf("secondbrain_vault_tags%s %d", l, v.Stats.Tags))
		links = append(links, fmt.Sprintf("secondbrain_vault_links%s %d", l, v.Stats.Links))
		broken = append(broken, fmt.Sprintf("secondbrain_vault_broken_links%s %d", l, v.Stats.BrokenLinks))
		orphans = append(orphans, fmt.Sprintf("secondbrain_vault_orphans%s %d", l, v.Stats.Orphans))
		tasks = append(tasks, fmt.Sprintf("secondbrain_vault_open_tasks%s %d", l, v.Stats.OpenTasks))
		atts = append(atts, fmt.Sprintf("secondbrain_vault_attachments%s %d", l, v.Stats.Attachments))
	}
	write("secondbrain_vault_notes", "Markdown notes in a vault.", "gauge", notes...)
	write("secondbrain_vault_words", "Words across a vault's notes.", "gauge", words...)
	write("secondbrain_vault_bytes", "Bytes across a vault's notes.", "gauge", bytes...)
	write("secondbrain_vault_tags", "Distinct tags in a vault.", "gauge", tags...)
	write("secondbrain_vault_links", "Internal links in a vault.", "gauge", links...)
	write("secondbrain_vault_broken_links", "Links pointing at a note that does not exist.", "gauge", broken...)
	write("secondbrain_vault_orphans", "Notes with no link in or out.", "gauge", orphans...)
	write("secondbrain_vault_open_tasks", "Unchecked task boxes.", "gauge", tasks...)
	write("secondbrain_vault_attachments", "Non-Markdown files in a vault.", "gauge", atts...)

	var calls, dur, tbytes []string
	for _, k := range sortedKeys(m.toolCalls) {
		parts := strings.SplitN(k, "|", 2)
		calls = append(calls, fmt.Sprintf("secondbrain_tool_calls_total{tool=%q,outcome=%q} %d",
			parts[0], parts[1], m.toolCalls[k]))
	}
	for _, k := range sortedKeys(m.toolDuration) {
		dur = append(dur, fmt.Sprintf("secondbrain_tool_duration_seconds_total{tool=%q} %.3f", k, m.toolDuration[k]))
	}
	for _, k := range sortedKeys(m.toolBytes) {
		tbytes = append(tbytes, fmt.Sprintf("secondbrain_tool_result_bytes_total{tool=%q} %d", k, m.toolBytes[k]))
	}
	write("secondbrain_tool_calls_total", "Tool calls by tool and outcome.", "counter", calls...)
	write("secondbrain_tool_duration_seconds_total", "Cumulative time spent in each tool.", "counter", dur...)
	write("secondbrain_tool_result_bytes_total", "Cumulative result bytes returned per tool.", "counter", tbytes...)

	var reqs []string
	for _, k := range sortedKeys(m.httpRequests) {
		parts := strings.SplitN(k, "|", 2)
		reqs = append(reqs, fmt.Sprintf("secondbrain_http_requests_total{path=%q,status=%q} %d",
			parts[0], parts[1], m.httpRequests[k]))
	}
	write("secondbrain_http_requests_total", "HTTP requests by route and status.", "counter", reqs...)

	var logins []string
	for _, k := range sortedKeys(m.logins) {
		logins = append(logins, fmt.Sprintf("secondbrain_logins_total{outcome=%q} %d", k, m.logins[k]))
	}
	write("secondbrain_logins_total", "Login attempts by outcome.", "counter", logins...)

	write("secondbrain_writes_total", "Mutating tool calls that actually wrote.", "counter",
		fmt.Sprintf("secondbrain_writes_total %d", m.writes))
	write("secondbrain_dry_runs_total", "Mutating tool calls that were dry runs.", "counter",
		fmt.Sprintf("secondbrain_dry_runs_total %d", m.dryRuns))
	write("secondbrain_truncated_results_total", "Results cut short by max_response_bytes.", "counter",
		fmt.Sprintf("secondbrain_truncated_results_total %d", m.truncated))
	write("secondbrain_index_updates_total", "Single-path index updates.", "counter",
		fmt.Sprintf("secondbrain_index_updates_total %d", m.indexUpdates))
	write("secondbrain_index_reconciles_total", "Full index reconciliations.", "counter",
		fmt.Sprintf("secondbrain_index_reconciles_total %d", m.indexReconciles))
	write("secondbrain_index_errors_total", "Index operations that failed.", "counter",
		fmt.Sprintf("secondbrain_index_errors_total %d", m.indexErrors))
	write("secondbrain_watch_events_total", "Filesystem changes picked up by the watcher.", "counter",
		fmt.Sprintf("secondbrain_watch_events_total %d", m.watchEvents))
	write("secondbrain_git_commits_total", "Commits made.", "counter",
		fmt.Sprintf("secondbrain_git_commits_total %d", m.gitCommits))
	write("secondbrain_git_failures_total", "Commits or pushes that failed.", "counter",
		fmt.Sprintf("secondbrain_git_failures_total %d", m.gitFailures))
	write("secondbrain_trash_purged_total", "Trashed copies removed after the retention window.", "counter",
		fmt.Sprintf("secondbrain_trash_purged_total %d", m.trashPurged))

	return b.String()
}
