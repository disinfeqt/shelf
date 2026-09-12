// Package library indexes the configured folders into the store and reads
// media metadata off the files themselves.
package library

import (
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rotisserie/eris"

	"shelf/internal/config"
	"shelf/internal/logx"
)

// Root is one configured folder, as written in config.json and as it
// resolves on this machine.
type Root struct {
	Spec string `json:"spec"` // as configured: a path or an smb:// URL
	Path string `json:"path"` // resolved absolute path ("" when unresolved)
	Name string `json:"name"` // last path segment, the label in the UI
	OK   bool   `json:"ok"`   // the folder is reachable right now
}

// Roots resolves every configured folder. An smb:// URL maps to the share's
// mount point under /Volumes and is mounted through Finder when it is not
// there yet, which is what makes a NAS share usable straight from config.
func Roots() []Root {
	var roots []Root
	for _, spec := range config.Current().Roots {
		roots = append(roots, resolveRoot(spec, true))
	}
	return roots
}

// ReadyRoots is Roots without the ones that cannot be reached.
func ReadyRoots() []Root {
	var ready []Root
	for _, root := range Roots() {
		if root.OK {
			ready = append(ready, root)
		}
	}
	return ready
}

// RootByPath finds the configured root a stored file belongs to.
func RootByPath(resolved string) (Root, bool) {
	for _, root := range Roots() {
		if root.Path == resolved {
			return root, true
		}
	}
	return Root{}, false
}

func resolveRoot(spec string, mount bool) Root {
	root := Root{Spec: spec}
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(strings.ToLower(spec), "smb://") {
		root.Path = smbMountPath(spec)
	} else if abs, err := filepath.Abs(spec); err == nil {
		root.Path = abs
	}
	if root.Path == "" {
		return root
	}
	root.Name = filepath.Base(root.Path)
	root.OK = dirExists(root.Path)
	if !root.OK && mount && strings.HasPrefix(strings.ToLower(spec), "smb://") {
		if err := mountSMB(spec); err != nil {
			logx.Warnf("Could not mount %s: %v", spec, err)
		}
		root.OK = dirExists(root.Path)
	}
	return root
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// smbMountPath turns smb://host/share/sub into /Volumes/share/sub, where
// macOS mounts the share.
func smbMountPath(spec string) string {
	u, err := url.Parse(spec)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	share, _ := url.PathUnescape(parts[0])
	rest := make([]string, 0, len(parts)-1)
	for _, p := range parts[1:] {
		seg, _ := url.PathUnescape(p)
		rest = append(rest, seg)
	}
	return filepath.Join(append([]string{"/Volumes", share}, rest...)...)
}

// mountSMB asks Finder to mount the share (credentials come from the
// keychain, or Finder prompts) and waits for the mount point to appear.
func mountSMB(spec string) error {
	if runtime.GOOS != "darwin" {
		return eris.New("automatic SMB mounting is only supported on macOS")
	}
	u, err := url.Parse(spec)
	if err != nil {
		return eris.Wrap(err, "invalid smb URL")
	}
	// Mount the share itself, not a folder inside it.
	share := *u
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	share.Path = "/" + path.Join(parts[:1]...)
	logx.Infof("Mounting %s…", share.String())
	if err := exec.Command("open", "-g", share.String()).Run(); err != nil {
		return eris.Wrap(err, "open failed")
	}
	target := smbMountPath(spec)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if dirExists(target) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return eris.Errorf("%s did not appear within 30s", target)
}
