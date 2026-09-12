// Package server is the local HTTP app: the JSON API and the embedded page.
package server

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/rotisserie/eris"

	"shelf/internal/logx"
)

//go:embed app.html
var appHTML []byte

//go:embed app.css
var appCSS []byte

//go:embed app.js
var appJS []byte

//go:embed manifest.webmanifest
var pwaManifest []byte

//go:embed icon-192.png
var icon192 []byte

//go:embed icon-512.png
var icon512 []byte

//go:embed apple-touch-icon.png
var appleTouchIcon []byte

func Start(addr string) error {
	http.HandleFunc("/", handleHome)
	http.HandleFunc("/app.css", serveAsset("text/css; charset=utf-8", appCSS))
	http.HandleFunc("/app.js", serveAsset("text/javascript; charset=utf-8", appJS))
	http.HandleFunc("/manifest.webmanifest", serveAsset("application/manifest+json", pwaManifest))
	http.HandleFunc("/icon-192.png", serveAsset("image/png", icon192))
	http.HandleFunc("/icon-512.png", serveAsset("image/png", icon512))
	http.HandleFunc("/apple-touch-icon.png", serveAsset("image/png", appleTouchIcon))

	http.HandleFunc("/api/items", handleItems)
	http.HandleFunc("/api/stats", handleStats)
	http.HandleFunc("/api/folders", handleFolders)
	http.HandleFunc("/api/status", handleStatus)
	http.HandleFunc("/api/rescan", handleRescan)
	http.HandleFunc("/api/logs", handleLogs)
	http.HandleFunc("/api/settings", handleSettings)
	http.HandleFunc("/api/reveal", handleReveal)
	http.HandleFunc("/api/delete", handleDelete)
	http.HandleFunc("/media/", handleMedia)
	http.HandleFunc("/thumb/", handleThumb)
	http.HandleFunc("/stream/", handleStream)

	urls, beyondLocalhost := dashboardURLs(addr)
	if beyondLocalhost {
		logx.Warn("Listening beyond this machine — anyone who can reach it can browse and delete the library; Shelf has no password")
	}
	logx.Infof("Shelf is ready — %s · keep this window running", strings.Join(urls, " or "))
	return http.ListenAndServe(addr, nil)
}

func handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(appHTML)
}

func serveAsset(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	}
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logx.Error(eris.Wrap(err, "Failed to encode JSON response"))
	}
}

// dashboardURLs turns a listen address into the URLs that reach the app,
// and reports whether that address is open to more than this machine.
func dashboardURLs(addr string) (urls []string, beyondLocalhost bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return []string{"http://" + addr}, true
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			host = "localhost"
		}
		return []string{"http://" + net.JoinHostPort(host, port)}, host != "localhost"
	}
	urls = []string{"http://" + net.JoinHostPort("localhost", port)}
	for _, ip := range lanIPs() {
		urls = append(urls, "http://"+net.JoinHostPort(ip, port))
	}
	return urls, true
}

func lanIPs() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var ips []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok {
				if ip := ipNet.IP.To4(); ip != nil && !ip.IsLinkLocalUnicast() {
					ips = append(ips, ip.String())
				}
			}
		}
	}
	return ips
}
