// Package config owns the persisted settings and their in-memory copy.
package config

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/rotisserie/eris"
)

// Config is the whole of config.json. Roots are the folders Shelf indexes:
// absolute paths, or smb://host/share[/folder] URLs that resolve to the
// share's mount point under /Volumes and mount it when needed.
type Config struct {
	Roots []string `json:"roots"`
	// Ignore lists folders the scanner skips. A bare name ("Trash") skips
	// any folder with that name at any depth; a path ("Shows/Extras") skips
	// that folder under any root, and everything beneath it.
	Ignore []string `json:"ignore"`
	// AllFeed changes only the combined feed; dedicated media tabs stay available.
	AllFeed AllFeed `json:"all_feed"`
}

type AllFeed struct {
	Photos bool `json:"photos"`
	Videos bool `json:"videos"`
}

const DefaultPath = "config.json"

var (
	mu      sync.RWMutex
	current = Default()
)

func Default() Config {
	return Config{Roots: []string{}, Ignore: []string{}, AllFeed: AllFeed{Photos: true, Videos: true}}
}

// Load reads the file, creating a default one when it does not exist yet.
func Load(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Update(path, Default())
		}
		return eris.Wrap(err, "failed to read config")
	}
	next := Default()
	if err := json.Unmarshal(content, &next); err != nil {
		return eris.Wrap(err, "failed to parse config")
	}
	return Update(path, next)
}

func Current() Config {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

func Update(path string, next Config) error {
	if next.Roots == nil {
		next.Roots = []string{}
	}
	if next.Ignore == nil {
		next.Ignore = []string{}
	}
	mu.Lock()
	defer mu.Unlock()
	content, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return eris.Wrap(err, "failed to encode config")
	}
	if err := os.WriteFile(path, append(content, '\n'), 0o644); err != nil {
		return eris.Wrap(err, "failed to write config")
	}
	current = next
	return nil
}

// SwapForTest replaces the in-memory config without touching disk.
func SwapForTest(next Config) (restore func()) {
	mu.Lock()
	previous := current
	current = next
	mu.Unlock()
	return func() {
		mu.Lock()
		current = previous
		mu.Unlock()
	}
}
