package library

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rotisserie/eris"
	"gorm.io/gorm"

	"shelf/internal/config"
	"shelf/internal/logx"
	"shelf/internal/store"
)

// Status is what the dashboard shows while a scan runs.
type Status struct {
	Scanning bool   `json:"scanning"`
	Phase    string `json:"phase"` // listing | probing | thumbs | ""
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	// Version bumps whenever a scan changed the library, so a page can tell
	// its grid is stale without diffing anything.
	Version  int64     `json:"version"`
	LastScan time.Time `json:"last_scan"`
	Roots    []Root    `json:"roots"`
	Ignore   []string  `json:"ignore"`
	FFmpeg   bool      `json:"ffmpeg"`
}

var (
	statusMu sync.Mutex
	status   Status
	running  atomic.Bool
	trigger  = make(chan struct{}, 1)
)

func CurrentStatus() Status {
	statusMu.Lock()
	defer statusMu.Unlock()
	s := status
	s.Roots = Roots()
	s.Ignore = config.Current().Ignore
	s.FFmpeg = FFmpeg() != ""
	return s
}

func setStatus(mutate func(s *Status)) {
	statusMu.Lock()
	mutate(&status)
	statusMu.Unlock()
}

// Rescan asks the worker for a scan now; a scan already queued is enough.
func Rescan() {
	select {
	case trigger <- struct{}{}:
	default:
	}
}

// StartWorker scans at startup and every interval after that.
func StartWorker(interval time.Duration) {
	ticker := time.NewTicker(interval)
	Rescan()
	for {
		select {
		case <-ticker.C:
		case <-trigger:
		}
		if err := Scan(); err != nil {
			logx.Error(eris.Wrap(err, "Scan failed"))
		}
	}
}

// Skipped directories: hidden ones, Synology's thumbnail trees and recycle
// bins, and macOS metadata.
func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "@eaDir" || name == "#recycle" || name == "$RECYCLE.BIN" || name == "System Volume Information"
}

// Ignored reports whether the user asked to skip this folder: by bare name
// anywhere, or by its path from the root. Case-insensitive, since the
// folders usually live on a share that is.
func Ignored(rel, name string) bool {
	for _, pattern := range config.Current().Ignore {
		pattern = strings.Trim(strings.TrimSpace(pattern), "/")
		if pattern == "" {
			continue
		}
		if strings.Contains(pattern, "/") {
			if strings.EqualFold(rel, pattern) || hasFoldPrefix(rel, pattern+"/") {
				return true
			}
		} else if strings.EqualFold(name, pattern) {
			return true
		}
	}
	return false
}

func hasFoldPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

type found struct {
	rel  string
	name string
	kind string
	ext  string
	size int64
	mod  time.Time
}

// Scan walks every reachable root, records what is there, drops what is
// gone, and reads metadata for anything new or changed. Two passes: the
// walk is cheap (one listing per folder) and gives the total, so the slow
// per-file probing can report progress against it.
func Scan() error {
	if !running.CompareAndSwap(false, true) {
		return nil
	}
	defer running.Store(false)

	gen := time.Now().UnixNano()
	setStatus(func(s *Status) { s.Scanning, s.Phase, s.Done, s.Total = true, "listing", 0, 0 })
	listed := 0
	defer setStatus(func(s *Status) { s.Scanning, s.Phase = false, ""; s.LastScan = time.Now() })

	changed := false
	var toProbe []int64
	for _, root := range Roots() {
		if !root.OK {
			logx.Warnf("Skipping %s — not reachable", root.Spec)
			continue
		}
		setStatus(func(s *Status) { s.Done = listed })
		entries, err := walkRoot(root.Path)
		if err != nil {
			logx.Error(eris.Wrapf(err, "Failed to list %s", root.Path))
			continue
		}
		listed += len(entries)
		// Index the listing in one transaction per root: a thousand upserts
		// as separate writes would take longer than the walk did.
		var added, updated int
		err = store.DB.Transaction(func(tx *gorm.DB) error {
			for _, e := range entries {
				var existing store.File
				res := tx.Where("root = ? AND rel_path = ?", root.Path, e.rel).Limit(1).Find(&existing)
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected == 0 {
					row := store.File{
						Root: root.Path, RelPath: e.rel, Dir: dirOf(e.rel), Name: e.name, Ext: e.ext, Kind: e.kind,
						Size: e.size, ModTime: e.mod, AddedAt: time.Now(), Seen: gen,
					}
					if err := tx.Create(&row).Error; err != nil {
						return err
					}
					toProbe = append(toProbe, row.ID)
					added++
					continue
				}
				updates := map[string]any{"seen": gen}
				if existing.Size != e.size || !existing.ModTime.Equal(e.mod) || !existing.Probed {
					updates["size"], updates["mod_time"], updates["probed"] = e.size, e.mod, false
					updates["name"], updates["ext"], updates["kind"], updates["dir"] = e.name, e.ext, e.kind, dirOf(e.rel)
					toProbe = append(toProbe, existing.ID)
					if existing.Probed {
						updated++
					}
				}
				if err := tx.Model(&store.File{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return eris.Wrapf(err, "failed to index %s", root.Path)
		}
		// Whatever the walk did not see is gone. Only for a root that was
		// walked: an unreachable share must not empty its own index.
		res := store.DB.Where("root = ? AND seen <> ?", root.Path, gen).Delete(&store.File{})
		if res.Error != nil {
			return eris.Wrap(res.Error, "failed to prune vanished files")
		}
		if added > 0 || updated > 0 || res.RowsAffected > 0 {
			changed = true
			logx.Infof("%s — %d files: %d new, %d changed, %d gone", root.Name, len(entries), added, updated, res.RowsAffected)
		}
	}

	if len(toProbe) > 0 {
		setStatus(func(s *Status) { s.Phase, s.Done, s.Total = "probing", 0, len(toProbe) })
		probeAll(toProbe)
		changed = true
	}
	if changed {
		setStatus(func(s *Status) { s.Version++ })
	}
	// Previews last: the library is browsable already, and this is the
	// slow part on a big share.
	renderMissingThumbs()
	return nil
}

func dirOf(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return ""
}

// dirEntry is one name in a folder, with what the scan needs to know.
type dirEntry struct {
	name  string
	isDir bool
	size  int64
	mod   time.Time
}

// walkRoot lists every media file under root, one folder at a time.
func walkRoot(root string) ([]found, error) {
	var out []found
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		entries, err := listDir(dir)
		if err != nil {
			return err
		}
		for _, d := range entries {
			name := d.name
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			if d.isDir {
				if skipDir(name) || Ignored(childRel, name) {
					continue
				}
				if err := walk(filepath.Join(dir, name), childRel); err != nil {
					logx.Warnf("Skipping %s: %v", childRel, err)
				}
				continue
			}
			if strings.HasPrefix(name, ".") {
				continue
			}
			kind, ext := KindOf(name)
			if kind == "" {
				continue
			}
			out = append(out, found{rel: childRel, name: name, kind: kind, ext: ext, size: d.size, mod: d.mod})
		}
		n := len(out)
		setStatus(func(s *Status) { s.Done = n })
		return nil
	}
	err := walk(root, "")
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, err
}

// A few probes at once: each is a header read over the network, and the
// NAS answers several in the time it answers one.
const probeWorkers = 4

func probeAll(ids []int64) {
	jobs := make(chan int64)
	var wg sync.WaitGroup
	var done atomic.Int64
	for i := 0; i < probeWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				var f store.File
				if err := store.DB.First(&f, id).Error; err != nil {
					continue
				}
				probe(&f)
				if err := store.DB.Model(&store.File{}).Where("id = ?", id).Select(
					"width", "height", "duration_ms", "v_codec", "a_codec", "direct", "probed",
				).Updates(&f).Error; err != nil {
					logx.Error(eris.Wrapf(err, "Failed to save metadata for %s", f.Name))
				}
				n := int(done.Add(1))
				setStatus(func(s *Status) { s.Done = n })
			}
		}()
	}
	for _, id := range ids {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
	logx.Infof("Read metadata for %d files", len(ids))
}

// FullPath is where a stored file lives on disk.
func FullPath(f *store.File) string {
	return filepath.Join(f.Root, filepath.FromSlash(f.RelPath))
}

// Remove deletes a file from disk and from the index.
func Remove(f *store.File) error {
	if err := os.Remove(FullPath(f)); err != nil && !os.IsNotExist(err) {
		return eris.Wrap(err, "failed to delete the file")
	}
	if err := store.DB.Delete(f).Error; err != nil {
		return eris.Wrap(err, "failed to drop the file from the index")
	}
	setStatus(func(s *Status) { s.Version++ })
	return nil
}
