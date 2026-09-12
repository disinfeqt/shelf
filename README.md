# Shelf

Browse the photos and videos on a folder or NAS share as one chronological
masonry feed, from any browser on your network. Folders stand in for
authors: every card names the folder it came from, and one click shows
everything in it.

## Run

```sh
go build -o shelf ./cmd/shelf
./shelf smb://nas/share            # or a plain path; remembered in config.json
open http://localhost:41010
```

Pass `-listen :41010` to reach it from a phone on the same network (there is
no password — trusted networks only). Folders can also be added and removed
in Settings.

An `smb://host/share[/folder]` root is mounted through Finder when it is not
already under `/Volumes`.

## What it does

- Indexes every photo, GIF and video under each folder into `shelf.db`,
  re-scanning every 15 minutes (or on Rescan in Settings). Metadata comes
  from the file headers, via `ffprobe` for video.
- Cards use a single small frame per file, rendered with `ffmpeg` into
  `cache/thumbs/`, so a NAS of large files stays quick to page through.
- Videos a browser can play (mp4, mov, webm with common codecs) play as
  they are. Anything else (mkv, surround audio…) is remuxed through
  `ffmpeg` on the fly — video copied, audio to stereo AAC — with seeking
  handled by the page.
- On a phone the lightbox becomes a vertical feed, one file per screen.
- Reveal in Finder, open the file directly, or delete it from disk.

`ffmpeg` and `ffprobe` are optional but strongly recommended:
`brew install ffmpeg`.
