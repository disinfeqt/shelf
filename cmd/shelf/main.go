// Shelf browses the photos and videos on a folder or NAS share as a
// chronological masonry feed, from any browser on the network.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/rotisserie/eris"

	"shelf/internal/config"
	"shelf/internal/library"
	"shelf/internal/logx"
	"shelf/internal/server"
	"shelf/internal/store"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:41010", `Address to listen on; pass ":41010" to reach Shelf from other devices on your network (no password — trusted networks only)`)
	rescan := flag.Duration("rescan", 15*time.Minute, "How often to re-index the folders")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: shelf [flags] [folder or smb://host/share ...]\n\nFolders given here are added to config.json. Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if err := config.Load(config.DefaultPath); err != nil {
		logx.Fatal(eris.Wrap(err, "Failed to load config"))
	}
	if flag.NArg() > 0 {
		next := config.Current()
		for _, spec := range flag.Args() {
			if !contains(next.Roots, spec) {
				next.Roots = append(next.Roots, spec)
			}
		}
		if err := config.Update(config.DefaultPath, next); err != nil {
			logx.Fatal(eris.Wrap(err, "Failed to save config"))
		}
	}
	if len(config.Current().Roots) == 0 {
		logx.Warn("No folders configured — pass one on the command line (a path or smb://host/share) or add it in Settings")
	}

	if err := store.Init("shelf.db"); err != nil {
		logx.Fatal(eris.Wrap(err, "Failed to open the index"))
	}
	logx.Info("Index ready")

	go library.StartWorker(*rescan)
	if err := server.Start(*listen); err != nil {
		logx.Fatal(eris.Wrap(err, "Server failed"))
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
