package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rotisserie/eris"

	"shelf/internal/library"
	"shelf/internal/logx"
	"shelf/internal/store"
)

// CacheDir holds generated thumbnails, beside the database.
var CacheDir = "cache"

// fileFromPath reads the id out of /prefix/<id>[/anything] and loads it.
func fileFromPath(w http.ResponseWriter, r *http.Request, prefix string) (*store.File, bool) {
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	idText, _, _ := strings.Cut(rest, "/")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return nil, false
	}
	var f store.File
	if err := store.DB.First(&f, id).Error; err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	return &f, true
}

// handleMedia serves the file itself: /media/<id>/<name>. The name is only
// there so a save-as gets a sensible filename; ServeFile handles ranges,
// which video seeking depends on.
func handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, ok := fileFromPath(w, r, "/media/")
	if !ok {
		return
	}
	full := library.FullPath(f)
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	if f.Kind == "video" && !f.Direct {
		// Not a browser format, but still the real bytes for a download.
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeFile(w, r, full)
}

// ---- Thumbnails --------------------------------------------------------

// iOS draws nothing for a video it has not played, and a NAS full of large
// photos is slow to page through at full size. ffmpeg renders one small
// frame per file into the cache the first time it is asked for.
const thumbWidth = 480

var thumbSlots = make(chan struct{}, 4)

func thumbPath(f *store.File) string {
	return filepath.Join(CacheDir, "thumbs", fmt.Sprintf("%d-%d.jpg", f.ID, f.ModTime.Unix()))
}

func handleThumb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, ok := fileFromPath(w, r, "/thumb/")
	if !ok {
		return
	}
	cached := thumbPath(f)
	if info, err := os.Stat(cached); err != nil || info.Size() == 0 {
		if err := renderThumb(r.Context(), f, cached); err != nil {
			logx.Error(eris.Wrapf(err, "Failed to render a thumbnail for %s", f.Name))
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=604800")
	http.ServeFile(w, r, cached)
}

func renderThumb(ctx context.Context, f *store.File, cached string) error {
	bin := library.FFmpeg()
	if bin == "" {
		return eris.New("ffmpeg is not installed")
	}
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		return eris.Wrap(err, "failed to create the thumbnail directory")
	}
	select {
	case thumbSlots <- struct{}{}:
	case <-ctx.Done():
		return eris.Wrap(ctx.Err(), "gave up waiting for a thumbnail slot")
	}
	defer func() { <-thumbSlots }()
	if info, err := os.Stat(cached); err == nil && info.Size() > 0 {
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
		// The opening frame of a film is black or a studio card; a tenth of
		// the way in is a scene. Short clips get their first moments.
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
		"-i", library.FullPath(f),
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
	logx.Infof("  [Thumb] %s (%s)", f.Name, time.Since(started).Round(time.Millisecond))
	return nil
}

// ---- Streaming ---------------------------------------------------------

// Browsers play mp4, mov and webm with common codecs and nothing else, so
// an mkv (or an mp4 with surround audio) is remuxed on the way out: video
// copied as it is, audio turned into stereo AAC, fragmented so playback can
// start before ffmpeg finishes. The stream has no length the browser can
// seek within, so the page seeks by reopening it with ?t=<seconds>.
var streamSlots = make(chan struct{}, 3)

func handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, ok := fileFromPath(w, r, "/stream/")
	if !ok {
		return
	}
	bin := library.FFmpeg()
	if bin == "" {
		http.Error(w, "ffmpeg is not installed", http.StatusNotImplemented)
		return
	}
	full := library.FullPath(f)
	if _, err := os.Stat(full); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}

	select {
	case streamSlots <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	defer func() { <-streamSlots }()

	at, _ := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	args := []string{"-nostdin", "-loglevel", "error"}
	if at > 0 {
		args = append(args, "-ss", strconv.FormatFloat(at, 'f', 2, 64))
	}
	args = append(args, "-i", full, "-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn",
		"-c:v", "copy", "-c:a", "aac", "-ac", "2", "-b:a", "192k")
	if f.VCodec == "hevc" {
		args = append(args, "-tag:v", "hvc1") // Safari refuses the other HEVC tag
	}
	args = append(args, "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")

	cmd := exec.CommandContext(r.Context(), bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "Could not start ffmpeg", http.StatusInternalServerError)
		return
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		http.Error(w, "Could not start ffmpeg", http.StatusInternalServerError)
		return
	}
	logx.Infof("  [Stream] %s from %.0fs", f.Name, at)
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64*1024)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if err != io.EOF && r.Context().Err() == nil {
				logx.Warnf("Stream of %s ended: %v", f.Name, err)
			}
			break
		}
	}
	_ = cmd.Wait()
}
