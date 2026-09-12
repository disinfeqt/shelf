# Shelf

Browse the photos and videos on a folder or NAS share as one chronological
masonry feed, from any browser on your network. Folders stand in for
authors: every card names the folder it came from, a tree of folders sits
beside the grid, and one click shows everything in any of them.

Single Go binary, SQLite index, no accounts, no password.

## Quick start

```sh
brew install ffmpeg            # optional but strongly recommended, see below
go build -o shelf ./cmd/shelf
./shelf smb://nas/share        # or a plain path; remembered in config.json
open http://localhost:41010
```

The folder given on the command line is saved to `config.json`, so after
the first run plain `./shelf` is enough. More folders can be added the
same way or in Settings.

Shelf reads `config.json` and writes `shelf.db` and `cache/` in the
directory it is started from. Run it from its own folder, or wrap it in an
alias that changes into it first:

```sh
alias s="cd ~/Sites/shelf && go build -o shelf ./cmd/shelf && ./shelf -listen :41010"
```

## Command line

```
shelf [flags] [folder or smb://host/share ...]
```

| Flag | Default | Meaning |
|---|---|---|
| `-listen` | `127.0.0.1:41010` | Address to listen on. `:41010` opens it to every device on your network. |
| `-rescan` | `15m` | How often folders are re-indexed. |

Flags go **before** any folder argument: Go stops parsing flags at the
first positional argument, and a flag placed after a folder is refused
rather than being saved as a folder.

Every positional argument is added to the `roots` list in `config.json`
if it is not there already.

## Configuration

`config.json`, created on first run beside the binary:

```json
{
  "roots": [
    "smb://192.168.0.110/Movies",
    "/Volumes/Photos/Family"
  ],
  "ignore": [
    "Trash",
    "Shows/Extras"
  ],
  "all_feed": {
    "photos": true,
    "videos": true
  }
}
```

### `roots`

The folders Shelf indexes, each with everything under it. Two forms:

- **A path** on this machine, absolute or relative to where Shelf runs.
- **An `smb://host/share[/folder]` URL.** It resolves to the share's mount
  point under `/Volumes` (`smb://nas/Movies/Shows` becomes
  `/Volumes/Movies/Shows`). If the share is not mounted, Shelf asks Finder
  to mount it and waits up to 30 seconds; credentials come from the
  keychain or Finder's prompt. macOS only.

A root that cannot be reached is shown as offline in the sidebar and
Settings, and its files stay in the index untouched until it comes back.
Nothing is ever pruned from a folder that could not be listed.
An unreadable subfolder defers updates for that root until a complete scan
can run. Preview generation uses only files seen in the current scan.
Deleted files still waiting for a preview are skipped and removed from the
index when their source root is reachable; an offline root retains its index.
Removing a root in Settings removes its index entries without deleting files.

Roots can also be added and removed in **Settings**, which rewrites this
file and starts a scan.

### `ignore`

Folders the scanner skips, with everything inside them. Case-insensitive.

- **A bare name** such as `Trash` skips any folder with that name at any
  depth, under every root.
- **A path** such as `Shows/Extras` skips that folder relative to any root.

Files inside a newly ignored folder drop out of the library on the next
scan, which starts as soon as the list is saved in Settings.

Always skipped regardless of this list: hidden folders and files
(`.something`), Synology's `@eaDir` and `#recycle`, `$RECYCLE.BIN`, and
`System Volume Information`.

### `all_feed`

Choose which media types appear in the **All** tab with **Settings → All
feed**. Photos (including GIFs) and videos each have a switch; both are on
by default. Turning a type off hides it from All, including folder, search,
and timeline views, while its dedicated **Photos** or **Videos** tab stays
available. Both switches may be turned off.

These preferences are saved in `config.json` and apply across devices.
They do not remove files from the index or trigger a rescan.

### Files on disk

| File | What it is |
|---|---|
| `config.json` | The settings above. Safe to edit by hand while Shelf is stopped. |
| `shelf.db` | The SQLite index. Delete it to force a full re-index. |
| `cache/thumbs/` | Rendered thumbnails, named by file id and modification time. Safe to delete; they are re-rendered on demand. |

## What gets indexed

Files are recognised by extension.

| Kind | Extensions |
|---|---|
| Photo | jpg, jpeg, png, webp, avif, bmp |
| GIF | gif |
| Video | mp4, m4v, mov, webm, mkv, avi, wmv, flv, ts, m2ts, mpg, mpeg |

Anything else is ignored. HEIC is deliberately left out because browsers
cannot display it.

### The scan

Runs at startup, every `-rescan` interval, and on **Rescan now** in
Settings. Two passes:

1. **Listing.** Every folder under every reachable root is read once. On
   macOS this uses `getattrlistbulk`, which returns names, sizes and dates
   for a whole directory in a few calls; on an SMB share that is several
   times faster than a stat per file. Files whose size and date are
   unchanged keep their metadata. Files no longer present are removed from
   the index.
2. **Metadata.** New or changed files have their headers read, four at a
   time: pixel size for photos, and for videos the size, duration and
   codecs via `ffprobe`. A scan interrupted by a restart picks up where it
   left off.

Progress shows in a strip above the grid. When a scan changes the library
while you are browsing, a **Library changed · refresh** button appears
rather than the grid reflowing under you; an empty grid refreshes itself.

## The interface

The interface uses monochrome light and dark themes, following the system
appearance. A slim toolbar combines location, media filters, and controls on
wide screens, with a compact two-row layout on smaller screens. Sidebar rows,
cards, and settings use readable type, flat controls, and subtle dividers.

### Sidebar

- **Search** matches file names and folder paths. Press **⌘K** (Mac) or
  **Ctrl+K** to focus it. The sidebar shows matching folders with counts
  for the current media and month filters, and expands their ancestors.
  Clear the search to restore the folder tree and its expanded state.
- **All media**, then a tree with each root as a top-level node and its
  subfolders nested beneath, each with a count that includes everything
  under it. Carets fold and unfold; the folder on screen is highlighted and
  its ancestors stay open.
- **Folder sorting** offers alphabetical A–Z or Z–A, recently or oldest
  modified, and most items. It sorts roots and each set of subfolders while
  preserving the hierarchy, and remembers your choice in this browser.
  Dates use the newest matching file in each folder and its descendants;
  dates and counts respect the current search, media, and month filters.
- **Timeline** lists months by file date; a month filters the grid.
- **Activity** is a live tail of Shelf's log. Each failed preview lists the
  full file path, expandable error details, **Find file** to locate it in
  Shelf, and **Reveal file** to show it on the machine running Shelf.
  A preview failure means no thumbnail was created; the original is unchanged
  and may still play. Activity keeps the latest 500 entries for this run.
  To reproduce an earlier failure after restarting, use **Settings → Rescan now**.
- **Settings** controls the media types shown in All, manages roots,
  ignored folders, and rescans, and warns if ffmpeg is missing.
- The foot shows the library's file count and total size, and a warning
  chip if there are no folders or one is offline.

On screens narrower than 720px the sidebar becomes a drawer behind the
menu button; choosing a folder closes it. The drawer supports keyboard
focus navigation and closes with Escape.

### Grid

- **Breadcrumbs** across the top show where you are (Library › Movies ›
  Shows › Bloods); each segment is clickable.
- **Tabs** narrow to photos (including GIFs) or videos. Counts are available
  on hover, with one total in the desktop toolbar. Equal-width tabs and a
  reserved count area keep controls in place when filters change. These tabs
  show their media even when it is hidden from All.
- **Sort:** newest or oldest file modification date, recently indexed, name,
  largest, longest or shortest video. Recently indexed means when Shelf first
  discovered a file, so an old file imported today can appear first. Files
  discovered in the same scan share an index date and sort by file date within
  that batch. Rescans preserve existing index dates; past index timestamps are
  unchanged. Cards show the index date in this mode, and the viewer shows both
  modification and index dates.
- **Grid density:** choose comfortable or compact cards in the desktop
  toolbar. Your preference is remembered on that browser; phones keep a
  two-column layout.
- Cards use a single small frame per file so a NAS of large files stays
  quick to page through. Videos show a play badge with duration and a
  resolution badge (720p, 1080p, 4K); browser-playable videos preview on
  hover. The folder and date ride over the foot of each card, and the file
  name appears on hover.
- Scrolling loads more; the URL carries every filter, so views can be
  bookmarked or shared to another device on the network.

### Viewer

Click a card to open it.

Videos scale to fill the available width or height, including small source
videos, while preserving their aspect ratio without cropping.

- **Desktop:** the file beside a panel with its name, folder (click to show
  that folder), dimensions, duration, size, codecs and date. Actions: more
  from this folder, reveal in Finder, open the file directly, delete from
  disk (with a confirmation step). Arrow keys move between files.
- **Phone:** a full-screen vertical feed, one file per screen, snapped to
  the viewport. Tap the middle of a video to pause, near an edge to bring
  up the details, double-tap a side to jump ten seconds. Muting follows you
  from file to file.

### Video playback

Browsers play mp4, m4v, mov and webm with common codecs (H.264, HEVC, VP8,
VP9, AV1 video; AAC, MP3, Opus, Vorbis, FLAC, ALAC audio) directly, with
native controls and seeking.

Anything else, such as mkv or an mp4 with surround audio, is remuxed
through ffmpeg on the fly: the video stream is copied as it is and the
first audio track is converted to stereo AAC, so most files start within a
second and use little CPU. Because such a stream has no length the browser
can seek within, Shelf provides its own play, seek rail and mute controls
and seeks by reopening the stream further in. The viewer notes "Played
through ffmpeg" on these files. Up to three streams run at once.

Whether a file plays directly is decided during the scan from its
container and codecs and stored in the index.

## Requirements

- Go 1.24 or newer to build.
- **ffmpeg and ffprobe**, optional but strongly recommended (`brew install
  ffmpeg`). Without them there are no thumbnails, video duration and size
  are unknown, and only browser-native formats play.
- macOS for automatic SMB mounting and the bulk directory listing; on other
  systems mount the share yourself and give Shelf the path.

## Network access

Shelf has no login. Listening on `:41010` exposes the whole library, and
the ability to delete files, to anyone who can reach that port. Use it on
a trusted home network only.

## API

Everything the page uses is plain JSON under `/api/`, so it can be
scripted.

| Endpoint | Purpose |
|---|---|
| `GET /api/items?q=&root=&dir=&kind=&month=&sort=&page=` | Paged listing with per-kind counts. |
| `GET /api/folders?q=&kind=&month=&root=&dir=` | Matching folders with counts and newest file modification dates rolled up through their ancestors. |
| `GET /api/stats?root=` | Totals, size on disk, monthly histogram. |
| `GET /api/status` | Scan state, roots and their reachability, ignore list, whether ffmpeg is present. |
| `POST /api/rescan` | Start a scan. |
| `GET /api/settings`, `POST /api/settings` | Read or patch `roots`, `ignore`, and `all_feed`. |
| `POST /api/reveal {"id": n}` | Reveal a file in Finder. |
| `POST /api/delete {"id": n}` | Delete a file from disk and the index. |
| `GET /media/<id>/<name>` | The file itself, with range support. |
| `GET /thumb/<id>` | The cached thumbnail. |
| `GET /stream/<id>?t=<seconds>` | The remuxed stream from that offset. |
| `GET /api/logs?after=<id>` | Recent log lines. |
