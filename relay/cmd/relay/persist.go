package main

// Saving the history to disk, so it survives relay restarts and upgrades.
// One JSON file, written atomically: a crash mid-write never leaves a broken file.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const historyFile = "history.json"

// savedHistory is the file format. Bump Version when it changes incompatibly.
type savedHistory struct {
	Version  int                        `json:"version"`
	Clusters map[string]*clusterHistory `json:"clusters"`
}

// CheckWritable fails early, with the fix in the message, when the relay cannot write there.
// Volumes from a StorageClass are made writable by fsGroup; a node folder or NFS export is not.
func CheckWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w. The relay runs as user 65532: "+
			"on the node or NFS server, run: chown 65532:65532 <the folder>", dir, err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// SaveHistory writes all clusters' history to dir/history.json.
func (s *Store) SaveHistory(dir string) error {
	s.mu.RLock()
	data, err := json.Marshal(savedHistory{Version: 1, Clusters: s.history})
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	// Write a temporary file, flush it to disk, then rename it over the old one.
	// rename is atomic: readers see either the old file or the new one, never half of one.
	tmp, err := os.CreateTemp(dir, historyFile+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, historyFile))
}

// LoadHistory reads dir/history.json. A missing file is fine: first start.
func (s *Store) LoadHistory(dir string) (clusters int, err error) {
	data, err := os.ReadFile(filepath.Join(dir, historyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var saved savedHistory
	if err := json.Unmarshal(data, &saved); err != nil {
		return 0, fmt.Errorf("%s is not valid: %w", historyFile, err)
	}
	if saved.Version != 1 {
		return 0, fmt.Errorf("%s has format version %d, this relay reads version 1", historyFile, saved.Version)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, h := range saved.Clusters {
		if h != nil {
			s.history[id] = h
		}
	}
	return len(saved.Clusters), nil
}
