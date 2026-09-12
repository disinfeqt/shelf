package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"shelf/internal/store"
)

func seed(t *testing.T) {
	t.Helper()
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

	first := get(t, handleItems, "/api/items?kind=photo")["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "Shows", first["folder"])
	assert.Equal(t, "pic", first["title"])
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

func TestStats(t *testing.T) {
	seed(t)
	payload := get(t, handleStats, "/api/stats")
	totals := payload["totals"].(map[string]any)
	assert.EqualValues(t, 4, totals["files"])
	assert.EqualValues(t, 65, totals["bytes"])
	assert.EqualValues(t, 3, totals["folders"])
	assert.Len(t, payload["monthly"], 4)
}
