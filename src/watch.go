package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ---------------------------------------------------------------------------
// Watching
//
// The index has to survive the vault being edited by something other than this
// program, because it will be: Obsidian on a laptop, a git pull on a server,
// rsync from a phone. Without a watcher the first search after any of those is
// wrong, and a search engine that is quietly wrong is worse than none.
//
// Events are debounced. A single save in an editor produces several inotify
// events, and a git checkout produces hundreds; reacting to each one would
// spend more time reindexing than the index saves.
// ---------------------------------------------------------------------------

const debounceWindow = 400 * time.Millisecond

type Watcher struct {
	vault *Vault
	w     *fsnotify.Watcher
	stop  <-chan struct{}
}

func StartWatcher(v *Vault, stop <-chan struct{}) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{vault: v, w: fw, stop: stop}
	if err := w.addTree(v.Root); err != nil {
		fw.Close()
		return nil, err
	}
	go w.run()
	return w, nil
}

// addTree registers every visible directory. Hidden directories are skipped
// for the same reason they are invisible to the tools: .git churns constantly
// and would generate an event storm on every commit we make ourselves.
func (w *Watcher) addTree(root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != root && strings.HasPrefix(filepath.Base(p), ".") {
			return filepath.SkipDir
		}
		return w.w.Add(p)
	})
}

func (w *Watcher) run() {
	defer w.w.Close()
	pending := map[string]bool{}
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	armed := false

	for {
		select {
		case <-w.stop:
			return

		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			base := filepath.Base(ev.Name)
			if strings.HasPrefix(base, ".") || strings.HasPrefix(base, ".sbtmp-") {
				continue
			}
			if ev.Op&fsnotify.Create != 0 {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					_ = w.addTree(ev.Name)
					// A newly created directory may already be full - a
					// clone or an unzip arrives that way - so reconcile
					// rather than trust the events we may have missed.
					pending["*"] = true
				}
			}
			rel := w.vault.Rel(ev.Name)
			if rel == "." || strings.HasPrefix(rel, "..") {
				continue
			}
			pending[rel] = true
			if !armed {
				timer.Reset(debounceWindow)
				armed = true
			}

		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			logWarn("watch_error", map[string]any{"vault": w.vault.Name, "error": err.Error()})

		case <-timer.C:
			armed = false
			batch := pending
			pending = map[string]bool{}
			w.flush(batch)
		}
	}
}

func (w *Watcher) flush(batch map[string]bool) {
	if batch["*"] || len(batch) > 50 {
		if err := w.vault.idx.Reconcile(w.vault); err != nil {
			logWarn("reconcile_failed", map[string]any{"vault": w.vault.Name, "error": err.Error()})
		}
		return
	}
	for rel := range batch {
		if err := w.vault.idx.UpdatePath(w.vault, rel); err != nil {
			logDebug("watch_update_skipped", map[string]any{"path": rel, "error": err.Error()})
		}
	}
	logDebug("watch_flush", map[string]any{"vault": w.vault.Name, "paths": len(batch)})
}
