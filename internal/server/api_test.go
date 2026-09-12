package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"shelf/internal/config"
	"shelf/internal/logx"
	"shelf/internal/store"
)

func seed(t *testing.T) {
	t.Helper()
	t.Cleanup(config.SwapForTest(config.Default()))
	original := store.DB
	t.Cleanup(func() { store.DB = original })
	require.NoError(t, store.Init(":memory:"))
	base := time.Date(2025, 3, 1, 12, 0, 0, 0, time.Local)
	rows := []store.File{
		{Root: "/r", RelPath: "a.mp4", Dir: "", Name: "a.mp4", Ext: "mp4", Kind: "video", Size: 30, ModTime: base, DurationMs: 90000, Direct: true},
		{Root: "/r", RelPath: "Shows/S/e1.mkv", Dir: "Shows/S", Name: "e1.mkv", Ext: "mkv", Kind: "video", Size: 20, ModTime: base.AddDate(0, 1, 0), DurationMs: 30000},
		{Root: "/r", RelPath: "Shows/pic.jpg", Dir: "Shows", Name: "pic.jpg", Ext: "jpg", Kind: "photo", Size: 10, ModTime: base.AddDate(0, 2, 0)},
		{Root: "/other", RelPath: "z.gif", Dir: "", Name: "z.gif", Ext: "gif", Kind: "gif", Size: 5, ModTime: base.AddDate(0, 3, 0)},
	}
	require.NoError(t, store.DB.Create(&rows).Error)
}

func get(t *testing.T, h http.HandlerFunc, url string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, url, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return payload
}

func ids(payload map[string]any) []string {
	var out []string
	for _, it := range payload["items"].([]any) {
		out = append(out, it.(map[string]any)["name"].(string))
	}
	return out
}

func TestItemsFiltersAndSorts(t *testing.T) {
	seed(t)
	all := get(t, handleItems, "/api/items")
	assert.EqualValues(t, 4, all["total"])
	assert.Equal(t, []string{"z.gif", "pic.jpg", "e1.mkv", "a.mp4"}, ids(all)) // newest first
	counts := all["counts"].(map[string]any)
	assert.EqualValues(t, 2, counts["video"])

	assert.Equal(t, []string{"a.mp4", "e1.mkv", "pic.jpg", "z.gif"}, ids(get(t, handleItems, "/api/items?sort=oldest")))
	assert.Equal(t, []string{"a.mp4"}, ids(get(t, handleItems, "/api/items?sort=largest&kind=video&root=/r&page=1"))[:1])
	assert.Equal(t, []string{"pic.jpg", "e1.mkv"}, ids(get(t, handleItems, "/api/items?dir=Shows")))
	assert.Equal(t, []string{"e1.mkv"}, ids(get(t, handleItems, "/api/items?dir=Shows/S")))
	assert.Equal(t, []string{"e1.mkv"}, ids(get(t, handleItems, "/api/items?q=E1")))
	assert.Equal(t, []string{"z.gif"}, ids(get(t, handleItems, "/api/items?root=/other")))
	assert.Equal(t, []string{"a.mp4"}, ids(get(t, handleItems, "/api/items?month=2025-03")))

	first := get(t, handleItems, "/api/items?kind=photo&root=/r")["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "Shows", first["folder"])
	assert.Equal(t, "pic", first["title"])
}

func TestItemsRecentlyIndexedUsesImportBatchThenFileDate(t *testing.T) {
	seed(t)
	batch := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	oldDate := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.DB.Create(&[]store.File{
		{Root: "/imports", RelPath: "z-old.gif", Name: "z-old.gif", Kind: "gif", ModTime: oldDate, AddedAt: batch},
		{Root: "/imports", RelPath: "a-new.gif", Name: "a-new.gif", Kind: "gif", ModTime: batch, AddedAt: batch},
		{Root: "/imports", RelPath: "later-import.gif", Name: "later-import.gif", Kind: "gif", ModTime: oldDate, AddedAt: batch.Add(time.Hour)},
	}).Error)
	assert.Equal(t, []string{"later-import.gif", "a-new.gif", "z-old.gif"},
		ids(get(t, handleItems, "/api/items?root=/imports&sort=added")))
	assert.Equal(t, []string{"a-new.gif", "later-import.gif", "z-old.gif"},
		ids(get(t, handleItems, "/api/items?root=/imports&sort=newest")))
}

func TestAllFeedVisibilityKeepsDedicatedTabs(t *testing.T) {
	for _, tc := range []struct {
		name string
		feed config.AllFeed
		want []string
	}{
		{"both", config.AllFeed{Photos: true, Videos: true}, []string{"z.gif", "pic.jpg", "e1.mkv", "a.mp4"}},
		{"videos only", config.AllFeed{Videos: true}, []string{"e1.mkv", "a.mp4"}},
		{"photos only", config.AllFeed{Photos: true}, []string{"z.gif", "pic.jpg"}},
		{"neither", config.AllFeed{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed(t)
			cfg := config.Current()
			cfg.AllFeed = tc.feed
			t.Cleanup(config.SwapForTest(cfg))
			for _, path := range []string{"/api/items", "/api/items?kind=all"} {
				all := get(t, handleItems, path)
				assert.Equal(t, tc.want, ids(all))
				assert.EqualValues(t, len(tc.want), all["total"])
				counts := all["counts"].(map[string]any)
				assert.EqualValues(t, len(tc.want), counts["all"])
				assert.EqualValues(t, 2, counts["photo"])
				assert.EqualValues(t, 2, counts["video"])
			}
			assert.Equal(t, []string{"z.gif", "pic.jpg"}, ids(get(t, handleItems, "/api/items?kind=photo")))
			assert.Equal(t, []string{"e1.mkv", "a.mp4"}, ids(get(t, handleItems, "/api/items?kind=video")))
			assert.Equal(t, []string{"z.gif"}, ids(get(t, handleItems, "/api/items?kind=gif")))
			stats := get(t, handleStats, "/api/stats")["totals"].(map[string]any)
			assert.EqualValues(t, 4, stats["files"])
		})
	}
}

func TestAllFeedFiltersBeforePagination(t *testing.T) {
	seed(t)
	cfg := config.Current()
	cfg.AllFeed.Photos = false
	t.Cleanup(config.SwapForTest(cfg))
	var rows []store.File
	for i := 0; i < pageSize+3; i++ {
		for _, kind := range []string{"photo", "video"} {
			name := fmt.Sprintf("%03d-%s", i, kind)
			rows = append(rows, store.File{Root: "/paged", RelPath: name, Name: name, Kind: kind, Dir: "Collection", ModTime: time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)})
		}
	}
	require.NoError(t, store.DB.Create(&rows).Error)
	for page, size := range map[int]int{1: pageSize, 2: 3} {
		all := get(t, handleItems, fmt.Sprintf("/api/items?root=/paged&dir=Collection&q=0&month=2026-09&sort=name&page=%d", page))
		assert.EqualValues(t, pageSize+3, all["total"])
		assert.Len(t, ids(all), size)
		for _, name := range ids(all) {
			assert.True(t, strings.HasSuffix(name, "-video"))
		}
	}
}

func TestSettingsSaveAllFeedIndependently(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Cleanup(config.SwapForTest(config.Default()))
	post := func(body string) {
		rec := httptest.NewRecorder()
		handleSettings(rec, httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(body)))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	post(`{"all_feed":{"photos":false}}`)
	assert.Equal(t, config.AllFeed{Videos: true}, config.Current().AllFeed)
	post(`{"ignore":["Trash"]}`)
	assert.Equal(t, config.AllFeed{Videos: true}, config.Current().AllFeed)
	post(`{"all_feed":{"videos":false}}`)
	assert.Equal(t, config.AllFeed{}, config.Current().AllFeed)
	require.NoError(t, config.Load(config.DefaultPath))
	assert.Equal(t, config.AllFeed{}, config.Current().AllFeed)
	assert.Equal(t, []string{"Trash"}, config.Current().Ignore)
	settings := get(t, handleSettings, "/api/settings")
	assert.Equal(t, map[string]any{"photos": false, "videos": false}, settings["all_feed"])
	status := get(t, handleStatus, "/api/status")
	assert.Equal(t, settings["all_feed"], status["all_feed"])
	rec := httptest.NewRecorder()
	handleSettings(rec, httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"all_feed":{"photos":"no"}}`)))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, config.AllFeed{}, config.Current().AllFeed)
}

func TestFoldersRollUp(t *testing.T) {
	seed(t)
	payload := get(t, handleFolders, "/api/folders?root=/r")
	items := payload["items"].([]any)
	byDir := map[string]float64{}
	for _, it := range items {
		f := it.(map[string]any)
		byDir[f["dir"].(string)] = f["count"].(float64)
	}
	assert.Equal(t, map[string]float64{"": 3, "Shows": 2, "Shows/S": 1}, byDir)
}

func TestFolderModificationDatesFollowMatchingDescendants(t *testing.T) {
	seed(t)
	base := time.Date(2025, 3, 1, 12, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		query string
		want  map[string]time.Time
	}{
		{"", map[string]time.Time{"": base.AddDate(0, 2, 0), "Shows": base.AddDate(0, 2, 0), "Shows/S": base.AddDate(0, 1, 0)}},
		{"&kind=video", map[string]time.Time{"": base.AddDate(0, 1, 0), "Shows": base.AddDate(0, 1, 0), "Shows/S": base.AddDate(0, 1, 0)}},
		{"&q=e1", map[string]time.Time{"": base.AddDate(0, 1, 0), "Shows": base.AddDate(0, 1, 0), "Shows/S": base.AddDate(0, 1, 0)}},
		{"&month=2025-03", map[string]time.Time{"": base}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			payload := get(t, handleFolders, "/api/folders?root=/r"+tc.query)
			got := map[string]time.Time{}
			for _, raw := range payload["items"].([]any) {
				folder := raw.(map[string]any)
				modified, err := time.Parse(time.RFC3339, folder["mod_time"].(string))
				require.NoError(t, err)
				got[folder["dir"].(string)] = modified
			}
			require.Len(t, got, len(tc.want))
			for dir, want := range tc.want {
				assert.True(t, want.Equal(got[dir]), "%s: want %s, got %s", dir, want, got[dir])
			}
		})
	}
	cfg := config.Current()
	cfg.AllFeed.Photos = false
	t.Cleanup(config.SwapForTest(cfg))
	for _, raw := range get(t, handleFolders, "/api/folders?root=/r")["items"].([]any) {
		assert.Equal(t, base.AddDate(0, 1, 0).UTC().Format(time.RFC3339), raw.(map[string]any)["mod_time"])
	}
}

func TestFolderModificationDatesCompareInstants(t *testing.T) {
	seed(t)
	for _, tc := range []struct {
		name  string
		dates []string
		want  string
	}{
		{"timezones", []string{"2026-09-10T23:00:00+08:00", "2026-09-10T20:00:00Z"}, "2026-09-10T20:00:00Z"},
		{"before-epoch", []string{"1968-01-01T00:00:00Z", "1969-01-01T00:00:00Z"}, "1969-01-01T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := "/" + tc.name
			for i, date := range tc.dates {
				modified, err := time.Parse(time.RFC3339, date)
				require.NoError(t, err)
				name := fmt.Sprintf("%d.jpg", i)
				require.NoError(t, store.DB.Create(&store.File{
					Root: root, Dir: "Nested/Folder", RelPath: "Nested/Folder/" + name,
					Name: name, Kind: "photo", ModTime: modified,
				}).Error)
			}
			payload := get(t, handleFolders, "/api/folders?root="+root)
			require.Len(t, payload["items"], 3)
			for _, raw := range payload["items"].([]any) {
				assert.Equal(t, tc.want, raw.(map[string]any)["mod_time"])
			}
		})
	}
}

func TestFoldersMatchSearchAndFeedFilters(t *testing.T) {
	seed(t)
	byPath := func(url string) map[string]int {
		payload := get(t, handleFolders, url)
		out := map[string]int{}
		for _, raw := range payload["items"].([]any) {
			folder := raw.(map[string]any)
			out[folder["root"].(string)+":"+folder["dir"].(string)] = int(folder["count"].(float64))
		}
		assert.EqualValues(t, len(out), payload["total"])
		return out
	}
	for _, tc := range []struct {
		url  string
		want map[string]int
	}{
		{"/api/folders?q=E1", map[string]int{"/r:": 1, "/r:Shows": 1, "/r:Shows/S": 1}},
		{"/api/folders?q=shows", map[string]int{"/r:": 2, "/r:Shows": 2, "/r:Shows/S": 1}},
		{"/api/folders?q=shows&kind=photo", map[string]int{"/r:": 1, "/r:Shows": 1}},
		{"/api/folders?q=shows&kind=video&month=2025-04", map[string]int{"/r:": 1, "/r:Shows": 1, "/r:Shows/S": 1}},
		{"/api/folders?q=e1&root=/other", map[string]int{}},
		{"/api/folders?q=missing", map[string]int{}},
		{"/api/folders?q=z.gif&kind=photo", map[string]int{"/other:": 1}},
	} {
		t.Run(tc.url, func(t *testing.T) { assert.Equal(t, tc.want, byPath(tc.url)) })
	}
	cfg := config.Current()
	cfg.AllFeed.Photos = false
	t.Cleanup(config.SwapForTest(cfg))
	assert.Empty(t, byPath("/api/folders?q=pic"))
	assert.Equal(t, map[string]int{"/r:": 1, "/r:Shows": 1}, byPath("/api/folders?q=pic&kind=photo"))
	assert.Equal(t, map[string]int{"/r:": 1, "/r:Shows": 1, "/r:Shows/S": 1}, byPath("/api/folders?q=shows"))
}

func TestFolderSearchCountsAllPagesAndEscapesWildcards(t *testing.T) {
	seed(t)
	for i := 0; i < pageSize+7; i++ {
		name := fmt.Sprintf("match_%03d%%.jpg", i)
		require.NoError(t, store.DB.Create(&store.File{Root: "/search", Dir: "Travel/Coast", RelPath: "Travel/Coast/" + name, Name: name, Kind: "photo"}).Error)
	}
	require.NoError(t, store.DB.Create(&store.File{Root: "/search", Dir: "Other", RelPath: "Other/match-other.jpg", Name: "match-other.jpg", Kind: "photo"}).Error)
	for _, query := range []string{"match_", "%25"} {
		payload := get(t, handleFolders, "/api/folders?q="+query)
		require.Len(t, payload["items"], 3)
		for _, raw := range payload["items"].([]any) {
			assert.EqualValues(t, pageSize+7, raw.(map[string]any)["count"])
		}
	}
}

func TestStats(t *testing.T) {
	seed(t)
	payload := get(t, handleStats, "/api/stats")
	totals := payload["totals"].(map[string]any)
	assert.EqualValues(t, 4, totals["files"])
	assert.EqualValues(t, 65, totals["bytes"])
	assert.EqualValues(t, 3, totals["folders"])
	assert.Len(t, payload["monthly"], 4)
}

func TestActivityIncludesPreviewFailureContext(t *testing.T) {
	file := logx.FileRef{
		ID: 42, Root: "/synthetic", Dir: "Clips", RelPath: "Clips/broken.mp4",
		Path: "/synthetic/Clips/broken.mp4", Name: "broken.mp4", Kind: "video",
	}
	logx.WarnFile("Preview failed", "Synthetic decoder error", file)
	payload := get(t, handleLogs, "/api/logs")
	entries := payload["items"].([]any)
	entry := entries[len(entries)-1].(map[string]any)
	assert.Equal(t, "Preview failed", entry["msg"])
	assert.Equal(t, "Synthetic decoder error", entry["detail"])
	context := entry["file"].(map[string]any)
	assert.Equal(t, file.Path, context["path"])
	assert.Equal(t, file.RelPath, context["rel_path"])
	assert.EqualValues(t, file.ID, context["id"])
	assert.Equal(t, entry["id"], payload["last_id"])
}

func TestRevealRejectsStaleActivityFile(t *testing.T) {
	seed(t)
	root := t.TempDir()
	file := store.File{Root: root, RelPath: "new.mp4", Name: "new.mp4", Kind: "video"}
	require.NoError(t, os.WriteFile(filepath.Join(root, file.RelPath), []byte("synthetic"), 0o644))
	require.NoError(t, store.DB.Create(&file).Error)
	original := revealInFileManager
	t.Cleanup(func() { revealInFileManager = original })
	var revealed string
	revealInFileManager = func(path string) error { revealed = path; return nil }
	for _, tc := range []struct {
		root, path string
		status     int
	}{
		{root, "old.mp4", http.StatusNotFound},
		{"/other", file.RelPath, http.StatusNotFound},
		{root, file.RelPath, http.StatusOK},
		{"", "", http.StatusOK}, // existing ID-only clients
	} {
		revealed = ""
		body, err := json.Marshal(map[string]any{"id": file.ID, "root": tc.root, "rel_path": tc.path})
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		handleReveal(rec, httptest.NewRequest(http.MethodPost, "/api/reveal", strings.NewReader(string(body))))
		assert.Equal(t, tc.status, rec.Code)
		if tc.status == http.StatusOK {
			assert.Equal(t, filepath.Join(root, file.RelPath), revealed)
		} else {
			assert.Empty(t, revealed)
		}
	}
}
