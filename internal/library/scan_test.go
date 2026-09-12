package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"shelf/internal/config"
	"shelf/internal/store"
)

func TestScanIndexesAndPrunes(t *testing.T) {
	original := store.DB
	t.Cleanup(func() { store.DB = original })
	require.NoError(t, store.Init(":memory:"))
	root := t.TempDir()
	t.Cleanup(config.SwapForTest(config.Config{Roots: []string{root}, Ignore: []string{"trash", "Shows/Extras"}}))

	require.NoError(t, os.MkdirAll(filepath.Join(root, "Shows", ".hidden"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "@eaDir"), 0o755))
	write := func(rel string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte("x"), 0o644))
	}
	write("a.mp4")
	write("Shows/b.jpg")
	write("Shows/.hidden/c.jpg")
	write("@eaDir/d.jpg")
	write("notes.txt")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Shows", "Extras", "deep"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Shows", "Trash"), 0o755))
	write("Shows/Extras/deep/e.jpg")
	write("Shows/Trash/f.jpg")

	require.NoError(t, Scan())
	var rows []store.File
	require.NoError(t, store.DB.Order("rel_path").Find(&rows).Error)
	require.Len(t, rows, 2)
	assert.Equal(t, "Shows/b.jpg", rows[0].RelPath)
	assert.Equal(t, "Shows", rows[0].Dir)
	assert.Equal(t, "photo", rows[0].Kind)
	assert.Equal(t, "a.mp4", rows[1].RelPath)
	assert.Equal(t, "video", rows[1].Kind)
	assert.True(t, rows[1].Probed)
	assert.EqualValues(t, 1, CurrentStatus().Version)

	// A vanished file leaves the index; an unchanged scan changes nothing.
	require.NoError(t, os.Remove(filepath.Join(root, "a.mp4")))
	require.NoError(t, Scan())
	require.NoError(t, store.DB.Find(&rows).Error)
	assert.Len(t, rows, 1)
	assert.EqualValues(t, 2, CurrentStatus().Version)
	require.NoError(t, Scan())
	assert.EqualValues(t, 2, CurrentStatus().Version)
}

func TestScanGroupsNewFilesAndPreservesTheirImportDate(t *testing.T) {
	originalDB, originalStatus := store.DB, CurrentStatus()
	t.Cleanup(func() {
		store.DB = originalDB
		setStatus(func(s *Status) { *s = originalStatus })
	})
	require.NoError(t, store.Init(":memory:"))
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	cfg := config.Default()
	cfg.Roots = []string{firstRoot, secondRoot}
	t.Cleanup(config.SwapForTest(cfg))
	oldDate := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	newDate := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	write := func(root, name string, modified time.Time) {
		file := filepath.Join(root, name)
		require.NoError(t, os.WriteFile(file, []byte("synthetic gif"), 0o644))
		require.NoError(t, os.Chtimes(file, modified, modified))
	}
	// Reverse alphabetical discovery must not put the 2010 file first.
	write(firstRoot, "a-new.gif", newDate)
	write(firstRoot, "z-old.gif", oldDate)
	write(secondRoot, "other-root.gif", oldDate)
	before := time.Now()
	require.NoError(t, Scan())
	var rows []store.File
	require.NoError(t, store.DB.Order("added_at DESC, mod_time DESC").Find(&rows).Error)
	require.Len(t, rows, 3)
	assert.Equal(t, "a-new.gif", rows[0].Name)
	imported := rows[0].AddedAt
	assert.False(t, imported.Before(before))
	for _, row := range rows {
		assert.True(t, imported.Equal(row.AddedAt))
	}

	// A new scan can discover an old file, without re-adding existing files.
	write(firstRoot, "later-import.gif", oldDate)
	write(firstRoot, "a-new.gif", newDate.Add(time.Hour))
	require.NoError(t, Scan())
	require.NoError(t, store.DB.Order("added_at DESC, mod_time DESC").Find(&rows).Error)
	require.Len(t, rows, 4)
	assert.Equal(t, "later-import.gif", rows[0].Name)
	assert.True(t, rows[0].AddedAt.After(imported))
	for _, row := range rows[1:] {
		assert.True(t, imported.Equal(row.AddedAt), "%s was already indexed", row.Name)
	}
}

func TestScanRemovesUnconfiguredRootsWithoutDeletingTheirFiles(t *testing.T) {
	originalDB, originalStatus := store.DB, CurrentStatus()
	t.Cleanup(func() {
		store.DB = originalDB
		setStatus(func(s *Status) { *s = originalStatus })
	})
	require.NoError(t, store.Init(":memory:"))
	keptRoot, removedRoot := t.TempDir(), t.TempDir()
	for _, root := range []string{keptRoot, removedRoot} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "keep.gif"), []byte("synthetic gif"), 0o644))
	}
	cfg := config.Default()
	cfg.Roots = []string{keptRoot, removedRoot}
	t.Cleanup(config.SwapForTest(cfg))
	require.NoError(t, Scan())
	cfg.Roots = []string{keptRoot}
	t.Cleanup(config.SwapForTest(cfg))
	require.NoError(t, Scan())
	var files []store.File
	require.NoError(t, store.DB.Find(&files).Error)
	require.Len(t, files, 1)
	assert.Equal(t, keptRoot, files[0].Root)
	_, err := os.Stat(filepath.Join(removedRoot, "keep.gif"))
	assert.NoError(t, err)
	// Removing the last root clears only the index, too.
	cfg.Roots = nil
	t.Cleanup(config.SwapForTest(cfg))
	require.NoError(t, Scan())
	require.NoError(t, store.DB.Find(&files).Error)
	assert.Empty(t, files)
	_, err = os.Stat(filepath.Join(keptRoot, "keep.gif"))
	assert.NoError(t, err)
}

func TestWalkDoesNotTreatUnreadableSubfoldersAsComplete(t *testing.T) {
	root := t.TempDir()
	_, err := walkRootWith(root, func(dir string) ([]dirEntry, error) {
		if dir == root {
			return []dirEntry{{name: "visible.gif"}, {name: "unreadable", isDir: true}}, nil
		}
		return nil, os.ErrPermission
	})
	assert.ErrorIs(t, err, os.ErrPermission)
	assert.Contains(t, err.Error(), "unreadable")
}

func TestIgnored(t *testing.T) {
	t.Cleanup(config.SwapForTest(config.Config{Ignore: []string{"trash", "Shows/Extras/", " "}}))
	assert.True(t, Ignored("a/b/Trash", "Trash"))
	assert.True(t, Ignored("shows/extras", "extras"))
	assert.True(t, Ignored("Shows/Extras/deep", "deep"))
	assert.False(t, Ignored("Shows/Extras2", "Extras2"))
	assert.False(t, Ignored("Extras", "Extras"))
}

func TestKindOf(t *testing.T) {
	for name, kind := range map[string]string{"A.JPG": "photo", "x.mkv": "video", "y.gif": "gif", "z.txt": "", "w.heic": ""} {
		got, _ := KindOf(name)
		assert.Equal(t, kind, got, name)
	}
}

func TestSMBMountPath(t *testing.T) {
	assert.Equal(t, "/Volumes/Movies/Shows", smbMountPath("smb://192.168.0.110/Movies/Shows"))
	assert.Equal(t, "/Volumes/My Share", smbMountPath("smb://nas/My%20Share"))
	assert.Equal(t, "", smbMountPath("smb://nas/"))
}
