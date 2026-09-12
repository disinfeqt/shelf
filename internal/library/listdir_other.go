//go:build !darwin || !cgo

package library

import (
	"os"

	"github.com/rotisserie/eris"
)

// listDir reads a folder with one stat per file — fine on a local disk.
func listDir(dir string) ([]dirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, eris.Wrapf(err, "list %s", dir)
	}
	out := make([]dirEntry, 0, len(entries))
	for _, d := range entries {
		if d.IsDir() {
			out = append(out, dirEntry{name: d.Name(), isDir: true})
			continue
		}
		if !d.Type().IsRegular() {
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue
		}
		out = append(out, dirEntry{name: d.Name(), size: info.Size(), mod: info.ModTime()})
	}
	return out, nil
}
