package library

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"shelf/internal/config"
	"shelf/internal/logx"
	"shelf/internal/store"
)

func preparePreviewTest(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the synthetic ffmpeg fixture uses a POSIX shell")
	}
	findTools()
	originalDB, originalCache, originalFFmpeg := store.DB, CacheDir, ffmpegP
	originalStatus := CurrentStatus()
	t.Cleanup(func() {
		store.DB, CacheDir, ffmpegP = originalDB, originalCache, originalFFmpeg
		setStatus(func(s *Status) { *s = originalStatus })
	})
	require.NoError(t, store.Init(":memory:"))
	CacheDir = t.TempDir()
	ffmpegP = filepath.Join(t.TempDir(), "ffmpeg")
	require.NoError(t, os.WriteFile(ffmpegP, []byte("#!/bin/sh\n"+script), 0o755))
	root := t.TempDir()
	cfg := config.Default()
	cfg.Roots = []string{root}
	t.Cleanup(config.SwapForTest(cfg))
	return root
}

func TestPreviewFailuresLogFilesAndReasons(t *testing.T) {
	// Exercise decoder errors, a successful exit without an image, and an
	// output file. This executable never opens the media paths it receives.
	root := preparePreviewTest(t, `
for argument in "$@"; do
  case "$argument" in
    */broken.mp4) printf 'Synthetic decoder error\n' >&2; exit 1 ;;
    */empty.mp4) exit 0 ;;
  esac
  output="$argument"
done
printf 'synthetic image' > "$output"
`)
	files := []store.File{
		{Root: root, RelPath: "Clips/broken.mp4", Name: "broken.mp4", Kind: "video", ModTime: time.Now()},
		{Root: root, RelPath: "Clips/empty.mp4", Name: "empty.mp4", Kind: "video", ModTime: time.Now()},
		{Root: root, RelPath: "Clips/ok.mp4", Name: "ok.mp4", Kind: "video", ModTime: time.Now()},
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Clips"), 0o755))
	for _, file := range files {
		require.NoError(t, os.WriteFile(FullPath(&file), []byte("synthetic media"), 0o644))
	}
	require.NoError(t, store.DB.Create(&files).Error)
	var after int64
	if entries := logx.Recent(0); len(entries) > 0 {
		after = entries[len(entries)-1].ID
	}
	renderMissingThumbs(0)
	entries := logx.Recent(after)
	failures := map[string]logx.Entry{}
	for _, entry := range entries {
		if entry.File != nil {
			failures[entry.File.Name] = entry
		}
	}
	require.Len(t, failures, 2)
	for _, file := range files[:2] {
		entry := failures[file.Name]
		require.NotNil(t, entry.File)
		assert.Equal(t, "Preview failed", entry.Msg)
		assert.Equal(t, "warn", entry.Level)
		assert.Equal(t, file.ID, entry.File.ID)
		assert.Equal(t, root, entry.File.Root)
		assert.Equal(t, "Clips", entry.File.Dir)
		assert.Equal(t, file.RelPath, entry.File.RelPath)
		assert.Equal(t, FullPath(&file), entry.File.Path)
		assert.Equal(t, "video", entry.File.Kind)
		assert.False(t, HasThumb(&file))
	}
	assert.Contains(t, failures["broken.mp4"].Detail, "Synthetic decoder error")
	assert.Contains(t, failures["empty.mp4"].Detail, "without producing an image")
	assert.Contains(t, entries[len(entries)-1].Msg, "Previews: 1 created, 2 failed")
	assert.True(t, HasThumb(&files[2]))
	leftovers, err := filepath.Glob(filepath.Join(CacheDir, "thumbs", ".thumb-*"))
	require.NoError(t, err)
	assert.Empty(t, leftovers)
}

func TestMissingPreviewsArePrunedWithoutRetries(t *testing.T) {
	root := preparePreviewTest(t, `printf invoked > "$SHELF_TEST_FFMPEG_MARKER"; exit 1`)
	marker := filepath.Join(t.TempDir(), "invoked")
	t.Setenv("SHELF_TEST_FFMPEG_MARKER", marker)
	file := store.File{Root: root, RelPath: "removed.mp4", Name: "removed.mp4", Kind: "video", Seen: 123, ModTime: time.Now()}
	require.NoError(t, store.DB.Create(&file).Error)
	before := CurrentStatus().Version
	renderMissingThumbs(123)
	var count int64
	require.NoError(t, store.DB.Model(&store.File{}).Count(&count).Error)
	assert.Zero(t, count)
	assert.Greater(t, CurrentStatus().Version, before)
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err), "ffmpeg must not run for an absent file")
	// Neither another preview pass nor another scan can resurrect the row.
	renderMissingThumbs(123)
	require.NoError(t, Scan())
	require.NoError(t, Scan())
	require.NoError(t, store.DB.Model(&store.File{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestOfflineSourcesKeepTheirIndexAndSkipPreviews(t *testing.T) {
	root := preparePreviewTest(t, `printf invoked > "$SHELF_TEST_FFMPEG_MARKER"; exit 1`)
	marker := filepath.Join(t.TempDir(), "invoked")
	t.Setenv("SHELF_TEST_FFMPEG_MARKER", marker)
	file := store.File{Root: filepath.Join(root, "offline"), RelPath: "keep.mp4", Name: "keep.mp4", Kind: "video", Seen: 100, ModTime: time.Now()}
	cfg := config.Default()
	cfg.Roots = []string{file.Root}
	t.Cleanup(config.SwapForTest(cfg))
	require.NoError(t, store.DB.Create(&file).Error)
	// Old rows are excluded even before source checks run.
	renderMissingThumbs(101)
	require.NoError(t, Scan())
	assert.ErrorIs(t, RenderThumb(context.Background(), &file, true), ErrPreviewUnavailable)
	var count int64
	require.NoError(t, store.DB.Model(&store.File{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err))
}

func TestDeletedQueuedPreviewCannotUseAReusedID(t *testing.T) {
	root := preparePreviewTest(t, `printf invoked > "$SHELF_TEST_FFMPEG_MARKER"; exit 1`)
	marker := filepath.Join(t.TempDir(), "invoked")
	t.Setenv("SHELF_TEST_FFMPEG_MARKER", marker)
	file := store.File{Root: root, RelPath: "removed.mp4", Kind: "video", ModTime: time.Now()}
	require.NoError(t, store.DB.Create(&file).Error)
	require.NoError(t, store.DB.Delete(&file).Error)
	assert.ErrorIs(t, RenderThumb(context.Background(), &file, true), ErrPreviewUnavailable)
	replacement := store.File{ID: file.ID, Root: root, RelPath: "replacement.mp4", Kind: "video", ModTime: file.ModTime}
	require.NoError(t, store.DB.Create(&replacement).Error)
	assert.ErrorIs(t, RenderThumb(context.Background(), &file, true), ErrPreviewUnavailable)
	var current store.File
	require.NoError(t, store.DB.First(&current, file.ID).Error)
	assert.Equal(t, "replacement.mp4", current.RelPath)
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err))
}

func TestSourceRemovedDuringFFmpegIsNotADecoderFailure(t *testing.T) {
	root := preparePreviewTest(t, `
previous=""
for argument in "$@"; do
  if [ "$previous" = "-i" ]; then rm -- "$argument"; fi
  previous="$argument"
done
printf 'No such file or directory\n' >&2
exit 1
`)
	file := store.File{Root: root, RelPath: "removed.mp4", Name: "removed.mp4", Kind: "video", ModTime: time.Now(), Seen: 123}
	require.NoError(t, os.WriteFile(FullPath(&file), []byte("synthetic media"), 0o644))
	require.NoError(t, store.DB.Create(&file).Error)
	var after int64
	if entries := logx.Recent(0); len(entries) > 0 {
		after = entries[len(entries)-1].ID
	}
	renderMissingThumbs(123)
	for _, entry := range logx.Recent(after) {
		assert.NotEqual(t, "Preview failed", entry.Msg)
		assert.NotEqual(t, "warn", entry.Level)
	}
	var count int64
	require.NoError(t, store.DB.Model(&store.File{}).Count(&count).Error)
	assert.Zero(t, count)
	assert.False(t, HasThumb(&file))
}
