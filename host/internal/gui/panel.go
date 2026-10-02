// Package gui serves the control panel over HTTP as well as in the native
// window.
//
// The panel is the same document in both cases. In the window the Go side
// injects a bridge and keeps the admin token to itself; in a browser the page
// fetches the admin API directly and the user pastes the token once. Serving it
// costs nothing, needs no CGO, and means the panel can be opened in a browser on
// the machine that runs the host — which is the only machine the admin API
// answers on, since it is loopback-only (docs/SECURITY.md).
//
// This file has no build constraints on purpose: the fallback must exist in a
// CGO_ENABLED=0 binary too, because that build has no window at all and the
// browser is then the only way to see the panel.
package gui

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed panel.html
var panelPage string

// AcceptsHTML reports whether a request is a browser asking for a page rather
// than a client asking what this endpoint is.
//
// `mobiledeck status` and curl fetch this URL to answer "is the host up?", and
// they must keep getting the plain-text summary. A browser sends
// `Accept: text/html`, which is the distinction.
func AcceptsHTML(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// ServePanel writes the control panel.
//
// The page is served without caching: it is read from the binary at start-up, so
// a stale copy in a browser would show a panel the running host does not have.
func ServePanel(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(panelPage))
}
