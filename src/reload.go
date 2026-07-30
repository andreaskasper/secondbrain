package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// watchConfig reloads on file change and on SIGHUP. A configuration that does
// not parse or validate is rejected and the previous one keeps running - a bad
// edit must never take the server down, because a restart would log everyone
// out.
func (s *Server) watchConfig(path string) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	last := statOf(path)

	for {
		select {
		case <-s.stop:
			return

		case <-hup:
			s.reload(path, "sighup")

		case <-ticker.C:
			cur := statOf(path)
			if cur == last {
				continue
			}
			last = cur
			s.reload(path, "file_changed")
		}
	}
}

// fingerprint captures mtime, size and inode, so an atomic replace (which
// changes the inode without necessarily changing size) is also detected.
type fingerprint struct {
	mtime int64
	size  int64
	ino   uint64
}

func statOf(path string) fingerprint {
	fi, err := os.Stat(path)
	if err != nil {
		return fingerprint{}
	}
	f := fingerprint{mtime: fi.ModTime().UnixNano(), size: fi.Size()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		f.ino = uint64(st.Ino)
	}
	return f
}

// reload swaps in a new configuration. Only the parts that can change safely
// while the server is running are swapped: users, limits and origins. The data
// directory and the public URL are not, because vaults and issued tokens are
// bound to them.
func (s *Server) reload(path, reason string) {
	next, err := LoadConfig(path)
	if err != nil {
		logError("config_reload_rejected", map[string]any{"reason": reason, "error": err.Error()})
		return
	}
	cur := s.Config()
	if next.DataDir != cur.DataDir {
		logWarn("config_reload_partial", map[string]any{
			"reason": reason, "ignored": "data_dir cannot change while running",
		})
		next.DataDir = cur.DataDir
	}
	if next.PublicURL != cur.PublicURL {
		logWarn("config_reload_partial", map[string]any{
			"reason": reason, "ignored": "public_url cannot change while running",
		})
		next.PublicURL = cur.PublicURL
	}
	s.setConfig(next)
	s.loginLimiter = NewKeyedLimiter(next.LoginRateLimit)
	logInfo("config_reloaded", map[string]any{
		"reason": reason, "users": len(next.Users), "source": next.Source,
	})
}
