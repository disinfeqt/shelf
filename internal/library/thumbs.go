package library

import (
	"context"
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
	if out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput(); err != nil {
		return eris.Wrapf(err, "ffmpeg failed: %s", strings.TrimSpace(string(out)))
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
func renderMissingThumbs() {
	if FFmpeg() == "" {
		return
	}
	var files []store.File
	if err := store.DB.Where("kind IN ?", []string{"photo", "video"}).
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
	var done, failed atomic.Int64
	for i := 0; i < cap(backgroundSlots); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if err := RenderThumb(context.Background(), f, true); err != nil {
					failed.Add(1)
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
	if n := failed.Load(); n > 0 {
		logx.Warnf("Rendered %d previews; %d files gave no frame", len(todo)-int(n), n)
	} else {
		logx.Infof("Rendered %d previews", len(todo))
	}
}
