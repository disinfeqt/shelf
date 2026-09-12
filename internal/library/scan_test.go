package library

import (
	"os"
	"path/filepath"
	"testing"

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
