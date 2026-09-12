package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rotisserie/eris"

	"shelf/internal/logx"
	"shelf/internal/store"
)

// CacheDir holds generated thumbnails, beside the database.
var CacheDir = "cache"

// thumbWidth is generous for a retina card and small enough that thousands
// of frames stay negligible on disk.
const thumbWidth = 480

// ThumbPath is where the frame for one file belongs. The name carries the
// file's date and size, so a replaced file never shows a stale frame.
func ThumbPath(f *store.File) string {
	return filepath.Join(CacheDir, "thumbs", fmt.Sprintf("%d-%d-%d.jpg", f.ID, f.ModTime.Unix(), f.Size))
}

// HasThumb reports whether a usable frame is already cached.
func HasThumb(f *store.File) bool {
	info, err := os.Stat(ThumbPath(f))
	return err == nil && info.Size() > 0
}

// Two pools: a scroll's own requests must not queue behind the background
// pass filling in the rest of the library.
var (
	thumbSlots      = make(chan struct{}, 4)
	backgroundSlots = make(chan struct{}, 2)
)

// ErrPreviewUnavailable means a queued file vanished or its index entry
// changed. It is not a decoder failure and should not be logged as one.
var ErrPreviewUnavailable = errors.New("preview source is no longer available")

func forgetMissingPreview(f *store.File) error {
	// A disconnected share must retain its index. Only forget an absent file
	// while its source root is still reachable; never touch source files.
	if !dirExists(f.Root) {
		return ErrPreviewUnavailable
	}
	res := store.DB.Where("id = ? AND root = ? AND rel_path = ? AND mod_time = ? AND size = ?",
		f.ID, f.Root, f.RelPath, f.ModTime, f.Size).Delete(&store.File{})
	if res.Error != nil {
		return eris.Wrap(res.Error, "failed to remove a missing file from the index")
	}
	if res.RowsAffected > 0 {
		setStatus(func(s *Status) { s.Version++ })
		logx.Infof("Removed missing file from index: %s", FullPath(f))
	}
	return ErrPreviewUnavailable
}

func checkPreviewSource(f *store.File) error {
	var current int64
	if err := store.DB.Model(&store.File{}).
		Where("id = ? AND root = ? AND rel_path = ? AND mod_time = ? AND size = ?", f.ID, f.Root, f.RelPath, f.ModTime, f.Size).
		Count(&current).Error; err != nil {
		return eris.Wrap(err, "failed to check the queued preview")
	}
	if current == 0 {
		return ErrPreviewUnavailable
	}
	info, err := os.Stat(FullPath(f))
	if os.IsNotExist(err) {
		return forgetMissingPreview(f)
	}
	if err != nil {
		return eris.Wrap(err, "could not read the preview source")
	}
	if !info.Mode().IsRegular() {
		return ErrPreviewUnavailable
	}
	return nil
}

// RenderThumb draws one small frame for f into the cache. Photos are
// scaled; videos give a frame a tenth of the way in, since a film's opening
// frame is black or a studio card.
func RenderThumb(ctx context.Context, f *store.File, background bool) error {
	bin := FFmpeg()
	if bin == "" {
		return eris.New("ffmpeg is not installed")
	}
	cached := ThumbPath(f)
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		return eris.Wrap(err, "failed to create the thumbnail directory")
	}
	slots := thumbSlots
	if background {
		slots = backgroundSlots
	}
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return eris.Wrap(ctx.Err(), "gave up waiting for a thumbnail slot")
	}
	defer func() { <-slots }()
	if err := checkPreviewSource(f); err != nil {
		return err
	}
	if HasThumb(f) {
		return nil // another request finished it while this one queued
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tmp, err := os.CreateTemp(filepath.Dir(cached), ".thumb-*.jpg")
	if err != nil {
		return eris.Wrap(err, "failed to create a temporary file")
	}
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmp.Name()) }()

	args := []string{"-nostdin", "-loglevel", "error", "-y"}
	if f.Kind == "video" {
		at := float64(f.DurationMs) / 1000 * 0.1
		if at > 180 {
			at = 180
		}
		if at < 0.5 {
			at = 0.5
		}
		args = append(args, "-ss", strconv.FormatFloat(at, 'f', 2, 64))
	}
	args = append(args,
		"-i", FullPath(f),
		"-frames:v", "1",
		"-vf", `scale=min(`+strconv.Itoa(thumbWidth)+`\,iw):-2`,
		"-q:v", "5", "-f", "image2", tmp.Name(),
	)
	out, renderErr := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	// The source or index row may have changed while ffmpeg ran. A removed
	// file must not publish a stale thumbnail or become a decoder warning.
	if err := checkPreviewSource(f); err != nil {
		return err
	}
	if renderErr != nil {
		if ctx.Err() != nil {
			return eris.Wrap(ctx.Err(), "preview generation timed out or was cancelled")
		}
		return eris.Wrapf(renderErr, "ffmpeg failed: %s", strings.TrimSpace(string(out)))
	}
	info, err := os.Stat(tmp.Name())
	if err != nil {
		return eris.Wrap(err, "failed to read the generated thumbnail")
	}
	if info.Size() == 0 {
		return eris.New("ffmpeg finished without producing an image; no decodable frame was available at the preview position")
	}
	if err := os.Rename(tmp.Name(), cached); err != nil {
		return eris.Wrap(err, "failed to store the thumbnail")
	}
	if !background {
		logx.Infof("  [Thumb] %s (%s)", f.Name, time.Since(started).Round(time.Millisecond))
	}
	return nil
}

// renderMissingThumbs fills the cache for every file without a frame, so
// paging through the library never waits on ffmpeg. GIFs show themselves.
func renderMissingThumbs(generation int64) {
	if FFmpeg() == "" {
		return
	}
	var files []store.File
	if err := store.DB.Where("seen = ? AND kind IN ?", generation, []string{"photo", "video"}).
		Select("id", "root", "rel_path", "name", "kind", "size", "mod_time", "duration_ms").
		Find(&files).Error; err != nil {
		logx.Error(eris.Wrap(err, "Failed to list files for thumbnails"))
		return
	}
	var todo []store.File
	for i := range files {
		if !HasThumb(&files[i]) {
			todo = append(todo, files[i])
		}
	}
	if len(todo) == 0 {
		return
	}
	setStatus(func(s *Status) { s.Phase, s.Done, s.Total = "thumbs", 0, len(todo) })
	logx.Infof("Rendering %d previews in the background", len(todo))

	jobs := make(chan *store.File)
	var wg sync.WaitGroup
	var done, failed, skipped atomic.Int64
	for i := 0; i < cap(backgroundSlots); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if err := RenderThumb(context.Background(), f, true); err != nil {
					if errors.Is(err, ErrPreviewUnavailable) {
						skipped.Add(1)
					} else {
						failed.Add(1)
						logx.WarnFile("Preview failed", err.Error(), logx.FileRef{
							ID: f.ID, Root: f.Root, Dir: dirOf(f.RelPath), RelPath: f.RelPath,
							Path: FullPath(f), Name: f.Name, Kind: f.Kind,
						})
					}
				}
				n := int(done.Add(1))
				setStatus(func(s *Status) { s.Done = n })
			}
		}()
	}
	for i := range todo {
		jobs <- &todo[i]
	}
	close(jobs)
	wg.Wait()
	created := len(todo) - int(failed.Load()+skipped.Load())
	if n := failed.Load(); n > 0 {
		logx.Warnf("Previews: %d created, %d failed, %d unavailable files skipped. See the Preview failed entries in Activity for file paths and reasons.", created, n, skipped.Load())
	} else if n := skipped.Load(); n > 0 {
		logx.Infof("Previews: %d created; %d unavailable files skipped", created, n)
	} else {
		logx.Infof("Rendered %d previews", created)
	}
}
