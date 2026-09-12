package library

import (
	"context"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"

	"shelf/internal/logx"
	"shelf/internal/store"
)

// What counts as media, by extension. Browsers decode all the photo formats
// and the direct video containers themselves; every other video container
// still indexes and plays, remuxed through ffmpeg.
var (
	photoExts = map[string]bool{"jpg": true, "jpeg": true, "png": true, "webp": true, "avif": true, "bmp": true, "heic": false}
	gifExts   = map[string]bool{"gif": true}
	videoExts = map[string]bool{
		"mp4": true, "m4v": true, "mov": true, "webm": true,
		"mkv": true, "avi": true, "wmv": true, "flv": true, "ts": true, "m2ts": true, "mpg": true, "mpeg": true,
	}
	directContainers = map[string]bool{"mp4": true, "m4v": true, "mov": true, "webm": true}
	directVideo      = map[string]bool{"h264": true, "hevc": true, "vp8": true, "vp9": true, "av1": true}
	directAudio      = map[string]bool{"aac": true, "mp3": true, "opus": true, "vorbis": true, "flac": true, "alac": true, "": true}
)

// KindOf classifies a file name, or returns "" for anything Shelf ignores.
func KindOf(name string) (kind, ext string) {
	ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	switch {
	case photoExts[ext]:
		return "photo", ext
	case gifExts[ext]:
		return "gif", ext
	case videoExts[ext]:
		return "video", ext
	}
	return "", ext
}

var (
	toolOnce sync.Once
	ffmpegP  string
	ffprobeP string
)

func findTools() {
	toolOnce.Do(func() {
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			ffmpegP = p
		} else {
			logx.Warn("ffmpeg not found — no video previews, and only mp4/mov/webm will play")
		}
		if p, err := exec.LookPath("ffprobe"); err == nil {
			ffprobeP = p
		} else {
			logx.Warn("ffprobe not found — video sizes and durations will be unknown")
		}
	})
}

// FFmpeg is the ffmpeg binary, or "" when it is not installed.
func FFmpeg() string {
	findTools()
	return ffmpegP
}

// probe fills in the metadata fields of f from the file on disk. It
// returns an error only when the file is not there to open; a header it
// cannot make sense of just leaves the fields empty.
func probe(f *store.File) error {
	full := filepath.Join(f.Root, filepath.FromSlash(f.RelPath))
	fh, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			return err
		}
		fh = nil
	} else {
		defer fh.Close()
	}
	switch f.Kind {
	case "photo", "gif":
		if fh != nil {
			f.Width, f.Height = imageDimensions(fh)
		}
		f.Direct = true
	case "video":
		probeVideo(f, full)
	}
	f.Probed = true
	return nil
}

func imageDimensions(r io.Reader) (int, int) {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

type ffprobeOut struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func probeVideo(f *store.File, path string) {
	findTools()
	if ffprobeP == "" {
		f.Direct = directContainers[f.Ext]
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffprobeP,
		"-v", "error",
		"-show_entries", "stream=codec_type,codec_name,width,height:format=duration",
		"-of", "json", path).Output()
	if err != nil {
		logx.Warnf("ffprobe failed for %s: %v", f.Name, err)
		f.Direct = directContainers[f.Ext]
		return
	}
	var parsed ffprobeOut
	if err := json.Unmarshal(out, &parsed); err != nil {
		return
	}
	for _, s := range parsed.Streams {
		switch s.CodecType {
		case "video":
			if f.VCodec == "" {
				f.VCodec, f.Width, f.Height = s.CodecName, s.Width, s.Height
			}
		case "audio":
			if f.ACodec == "" {
				f.ACodec = s.CodecName
			}
		}
	}
	if secs, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil {
		f.DurationMs = int(secs * 1000)
	}
	f.Direct = directContainers[f.Ext] && directVideo[f.VCodec] && directAudio[f.ACodec]
}
