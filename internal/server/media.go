package server

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/rotisserie/eris"

	"shelf/internal/library"
	"shelf/internal/logx"
	"shelf/internal/store"
)

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

func handleThumb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, ok := fileFromPath(w, r, "/thumb/")
	if !ok {
		return
	}
	if !library.HasThumb(f) {
		if err := library.RenderThumb(r.Context(), f, false); err != nil {
			if r.Context().Err() == nil {
				logx.Error(eris.Wrapf(err, "Failed to render a thumbnail for %s", f.Name))
			}
			http.NotFound(w, r)
			return
		}
	}
	// The page asks for /thumb/<id>?v=<date and size>, so a frame can be
	// held for a long time and a changed file still gets a fresh one.
	w.Header().Set("Cache-Control", "public, max-age=2592000")
	http.ServeFile(w, r, library.ThumbPath(f))
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
