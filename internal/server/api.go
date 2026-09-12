package server

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rotisserie/eris"
	"gorm.io/gorm"

	"shelf/internal/config"
	"shelf/internal/library"
	"shelf/internal/logx"
	"shelf/internal/store"
)

const pageSize = 60

// item is a file as the page sees it.
type item struct {
	ID         int64     `json:"id"`
	Root       string    `json:"root"`
	RootName   string    `json:"root_name"`
	Dir        string    `json:"dir"`
	Folder     string    `json:"folder"` // the immediate folder's name; the root's at top level
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Title      string    `json:"title"` // name without the extension
	Ext        string    `json:"ext"`
	Kind       string    `json:"kind"`
	Size       int64     `json:"size"`
	ModTime    time.Time `json:"mod_time"`
	AddedAt    time.Time `json:"added_at"`
	Width      int       `json:"width,omitempty"`
	Height     int       `json:"height,omitempty"`
	DurationMs int       `json:"duration_ms,omitempty"`
	VCodec     string    `json:"vcodec,omitempty"`
	ACodec     string    `json:"acodec,omitempty"`
	Direct     bool      `json:"direct"`
}

func itemFrom(f *store.File) item {
	it := item{
		ID: f.ID, Root: f.Root, RootName: filepath.Base(f.Root), Dir: f.Dir, Path: f.RelPath,
		Name: f.Name, Title: strings.TrimSuffix(f.Name, filepath.Ext(f.Name)), Ext: f.Ext, Kind: f.Kind,
		Size: f.Size, ModTime: f.ModTime, AddedAt: f.AddedAt,
		Width: f.Width, Height: f.Height, DurationMs: f.DurationMs, VCodec: f.VCodec, ACodec: f.ACodec,
		Direct: f.Direct,
	}
	it.Folder = path.Base(f.Dir)
	if f.Dir == "" {
		it.Folder = it.RootName
	}
	return it
}

// filters are the query parameters every listing shares.
type filters struct {
	root  string
	q     string
	dir   string
	kind  string
	month string
}

func readFilters(r *http.Request) filters {
	return filters{
		root:  strings.TrimSpace(r.URL.Query().Get("root")),
		q:     strings.TrimSpace(r.URL.Query().Get("q")),
		dir:   strings.Trim(strings.TrimSpace(r.URL.Query().Get("dir")), "/"),
		kind:  r.URL.Query().Get("kind"),
		month: r.URL.Query().Get("month"),
	}
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// base applies everything but the kind filter, so the per-kind tab counts
// can share it.
func (f filters) base() *gorm.DB {
	q := store.DB.Model(&store.File{})
	if f.root != "" {
		q = q.Where("root = ?", f.root)
	}
	if f.q != "" {
		like := "%" + escapeLike(f.q) + "%"
		q = q.Where(`(name LIKE ? ESCAPE '\' OR dir LIKE ? ESCAPE '\')`, like, like)
	}
	if f.dir != "" {
		// The folder and everything under it.
		q = q.Where(`(dir = ? OR dir LIKE ? ESCAPE '\')`, f.dir, escapeLike(f.dir)+"/%")
	}
	if start, err := time.ParseInLocation("2006-01", f.month, time.Local); err == nil {
		q = q.Where("mod_time >= ? AND mod_time < ?", start, start.AddDate(0, 1, 0))
	}
	return q
}

func withKind(q *gorm.DB, kind string) *gorm.DB {
	switch kind {
	case "photo", "video", "gif":
		return q.Where("kind = ?", kind)
	}
	return q
}

func handleItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f := readFilters(r)
	order := "mod_time DESC, name ASC"
	switch r.URL.Query().Get("sort") {
	case "oldest":
		order = "mod_time ASC, name ASC"
	case "added":
		order = "added_at DESC, mod_time DESC"
	case "name":
		order = "name COLLATE NOCASE ASC"
	case "largest":
		order = "size DESC"
	case "longest":
		order = "duration_ms DESC, mod_time DESC"
	case "shortest":
		order = "CASE WHEN duration_ms > 0 THEN 0 ELSE 1 END, duration_ms ASC, mod_time DESC"
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	counts := map[string]int64{}
	for _, kind := range []string{"all", "photo", "video", "gif"} {
		var n int64
		if err := withKind(f.base(), kind).Count(&n).Error; err != nil {
			logx.Error(eris.Wrap(err, "Failed to count files"))
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		counts[kind] = n
	}
	total := counts["all"]
	if f.kind == "photo" || f.kind == "video" || f.kind == "gif" {
		total = counts[f.kind]
	}

	var rows []store.File
	if err := withKind(f.base(), f.kind).Order(order).
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		logx.Error(eris.Wrap(err, "Failed to list files"))
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	items := make([]item, 0, len(rows))
	for i := range rows {
		items = append(items, itemFrom(&rows[i]))
	}
	writeJSON(w, map[string]any{
		"total": total, "page": page, "page_size": pageSize, "counts": counts, "items": items,
	})
}

type folderStat struct {
	Root  string `json:"root"`
	Dir   string `json:"dir"`
	Name  string `json:"name"`
	Count int    `json:"count"`
	Depth int    `json:"depth"`
}

// folderStats counts files per folder, each folder including everything
// under it — the way the dir filter reads it.
func folderStats(root string) ([]folderStat, error) {
	type row struct {
		Root  string
		Dir   string
		Count int
	}
	var rows []row
	q := store.DB.Model(&store.File{}).Select("root, dir, COUNT(*) AS count").Group("root, dir")
	if root != "" {
		q = q.Where("root = ?", root)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	totals := map[[2]string]int{}
	for _, r := range rows {
		dir := r.Dir
		for {
			totals[[2]string{r.Root, dir}] += r.Count
			if dir == "" {
				break
			}
			if i := strings.LastIndex(dir, "/"); i >= 0 {
				dir = dir[:i]
			} else {
				dir = ""
			}
		}
	}
	out := make([]folderStat, 0, len(totals))
	for key, n := range totals {
		name := path.Base(key[1])
		depth := strings.Count(key[1], "/") + 1
		if key[1] == "" {
			name, depth = filepath.Base(key[0]), 0
		}
		out = append(out, folderStat{Root: key[0], Dir: key[1], Name: name, Count: n, Depth: depth})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Root != out[j].Root {
			return out[i].Root < out[j].Root
		}
		return out[i].Dir < out[j].Dir
	})
	return out, nil
}

func handleFolders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stats, err := folderStats(strings.TrimSpace(r.URL.Query().Get("root")))
	if err != nil {
		logx.Error(eris.Wrap(err, "Failed to count folders"))
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"total": len(stats), "items": stats})
}

type monthStat struct {
	Month string `json:"month"`
	Count int    `json:"count"`
}
type kindStat struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	root := strings.TrimSpace(r.URL.Query().Get("root"))
	type row struct {
		Kind    string
		Dir     string
		Size    int64
		ModTime time.Time
	}
	var rows []row
	q := store.DB.Model(&store.File{}).Select("kind", "dir", "size", "mod_time")
	if root != "" {
		q = q.Where("root = ?", root)
	}
	if err := q.Find(&rows).Error; err != nil {
		logx.Error(eris.Wrap(err, "Failed to query stats"))
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	var bytes int64
	var first, last time.Time
	months := map[string]int{}
	kinds := map[string]int{}
	dirs := map[string]bool{}
	for _, r := range rows {
		bytes += r.Size
		local := r.ModTime.In(time.Local)
		if first.IsZero() || local.Before(first) {
			first = local
		}
		if last.IsZero() || local.After(last) {
			last = local
		}
		months[local.Format("2006-01")]++
		kinds[r.Kind]++
		dirs[r.Dir] = true
	}
	monthly := []monthStat{}
	if !first.IsZero() {
		cursor := time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, time.Local)
		end := time.Date(last.Year(), last.Month(), 1, 0, 0, 0, 0, time.Local)
		for !cursor.After(end) {
			key := cursor.Format("2006-01")
			monthly = append(monthly, monthStat{Month: key, Count: months[key]})
			cursor = cursor.AddDate(0, 1, 0)
		}
	}
	kindList := []kindStat{}
	for _, k := range []string{"photo", "video", "gif"} {
		if kinds[k] > 0 {
			kindList = append(kindList, kindStat{Kind: k, Count: kinds[k]})
		}
	}
	payload := map[string]any{
		"totals": map[string]any{
			"files": len(rows), "bytes": bytes, "folders": len(dirs),
			"photos": kinds["photo"], "videos": kinds["video"], "gifs": kinds["gif"],
		},
		"monthly": monthly,
		"kinds":   kindList,
	}
	if !first.IsZero() {
		payload["first"] = first.Format(time.RFC3339)
		payload["last"] = last.Format(time.RFC3339)
	}
	writeJSON(w, payload)
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, library.CurrentStatus())
}

func handleRescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	library.Rescan()
	writeJSON(w, map[string]any{"ok": true})
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	items := logx.Recent(after)
	lastID := after
	if len(items) > 0 {
		lastID = items[len(items)-1].ID
	}
	writeJSON(w, map[string]any{"items": items, "last_id": lastID})
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"roots": library.Roots()})
	case http.MethodPost:
		var patch struct {
			Roots []string `json:"roots"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil || patch.Roots == nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		var roots []string
		for _, spec := range patch.Roots {
			if spec = strings.TrimSpace(spec); spec != "" {
				roots = append(roots, spec)
			}
		}
		if err := config.Update(config.DefaultPath, config.Config{Roots: roots}); err != nil {
			logx.Error(eris.Wrap(err, "Failed to save settings"))
			http.Error(w, "Failed to save settings", http.StatusInternalServerError)
			return
		}
		library.Rescan()
		writeJSON(w, map[string]any{"roots": library.Roots()})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// fileFromBody reads {"id": n} and loads the row.
func fileFromBody(w http.ResponseWriter, r *http.Request) (*store.File, bool) {
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return nil, false
	}
	var f store.File
	if err := store.DB.First(&f, req.ID).Error; err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return nil, false
	}
	return &f, true
}

var revealInFileManager = func(p string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", "-R", p).Run()
	case "windows":
		_ = exec.Command("explorer", "/select,"+p).Run()
		return nil
	default:
		return exec.Command("xdg-open", filepath.Dir(p)).Run()
	}
}

func handleReveal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, ok := fileFromBody(w, r)
	if !ok {
		return
	}
	full := library.FullPath(f)
	if _, err := os.Stat(full); err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	if err := revealInFileManager(full); err != nil {
		logx.Error(eris.Wrap(err, "Failed to reveal file"))
		http.Error(w, "Could not reveal the file", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, ok := fileFromBody(w, r)
	if !ok {
		return
	}
	if err := library.Remove(f); err != nil {
		logx.Error(eris.Wrapf(err, "Failed to delete %s", f.Name))
		http.Error(w, "Could not delete the file", http.StatusInternalServerError)
		return
	}
	logx.Infof("Deleted %s", f.RelPath)
	writeJSON(w, map[string]any{"ok": true})
}
