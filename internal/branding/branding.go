// Package branding holds the DUMB bear mascot: ASCII banners for terminals and
// the icons served over HTTP and embedded into desktop surfaces.
package branding

import (
	_ "embed"
	"net/http"
)

// Logo is the full ASCII bear with the DUMB wordmark.
//
//go:embed logo.txt
var Logo string

// LogoCompact is the small ASCII bear for narrow output.
//
//go:embed logo-compact.txt
var LogoCompact string

var (
	//go:embed assets/favicon.ico
	Favicon []byte
	//go:embed assets/icon-128.png
	IconPNG []byte
	//go:embed assets/apple-touch-icon.png
	AppleTouchIcon []byte
	// TrayICO is a multi-size (16-256 px) icon for the Windows notification area.
	//go:embed assets/tray.ico
	TrayICO []byte
)

// Handler serves one embedded icon with a long-lived cache header.
func Handler(contentType string, body []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(body)
	})
}

// Register adds /favicon.ico, /icon.png and /apple-touch-icon.png to mux.
func Register(mux *http.ServeMux) {
	mux.Handle("GET /favicon.ico", Handler("image/x-icon", Favicon))
	mux.Handle("GET /icon.png", Handler("image/png", IconPNG))
	mux.Handle("GET /apple-touch-icon.png", Handler("image/png", AppleTouchIcon))
}
