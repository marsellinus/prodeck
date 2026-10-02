//go:build cgo

// Package gui is the desktop control panel: a native window with a web view
// that drives the host through its own admin API.
//
// It is a thin shell on purpose. Every button in the panel calls the same
// loopback admin endpoint the CLI calls, so there is exactly one implementation
// of every operation and the GUI cannot drift from the command line. The panel
// is HTML because a layout editor is a grid of rectangles and a form, which is
// what a browser is good at, and because it keeps the whole thing to one binary
// with no toolkit to install (docs/adr/0011-desktop-gui.md).
package gui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	webview "github.com/webview/webview_go"
)

// Options configures the window.
type Options struct {
	// Addr is the loopback address the host is listening on.
	Addr string
	// Token is the per-process admin token.
	Token string
	// Title is the window title.
	Title string
	// Debug enables the web view's developer tools.
	Debug bool
}

// Run opens the window and blocks until it is closed.
//
// The host itself is started by the caller before this is called: the panel is a
// client of the running host, never a second implementation of it.
func Run(opts Options) error {
	if opts.Addr == "" || opts.Token == "" {
		return fmt.Errorf("gui: the host address and admin token are required")
	}
	if opts.Title == "" {
		opts.Title = "MobileDeck"
	}

	w := webview.New(opts.Debug)
	defer w.Destroy()
	w.SetTitle(opts.Title)
	w.SetSize(1180, 820, webview.HintNone)

	// The panel cannot reach the admin API itself: the API is loopback-only and
	// gated on a bearer token that must not be handed to JavaScript, where any
	// injected script could read it. So the web view is given one narrow
	// function that proxies a request, and the token stays on this side.
	api := &apiClient{addr: opts.Addr, token: opts.Token, http: &http.Client{Timeout: 15 * time.Second}}
	if err := w.Bind("md", api.call); err != nil {
		return fmt.Errorf("gui: binding the admin bridge: %w", err)
	}

	w.SetHtml(panelPage)
	w.Run()
	return nil
}

// apiClient proxies panel calls to the host's loopback admin API.
type apiClient struct {
	addr  string
	token string
	http  *http.Client
}

// call performs one admin request on behalf of the panel.
//
// It is the only function exposed to JavaScript. The method is restricted to the
// verbs the panel actually needs and the path must stay under the admin prefix,
// so a bug or an injected script cannot turn this into a general-purpose
// request forwarder.
func (c *apiClient) call(method, path, body string) (string, error) {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return "", fmt.Errorf("unsupported method %q", method)
	}
	if !strings.HasPrefix(path, "/api/v1/admin/") {
		return "", fmt.Errorf("the panel may only call /api/v1/admin/")
	}

	var reader io.Reader
	if body != "" && body != "null" {
		reader = bytes.NewReader([]byte(body))
	}

	req, err := http.NewRequest(method, "http://"+c.addr+path, reader)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach the host: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}

	// The status travels with the body so the panel can show a validation error
	// next to the field that caused it, rather than throwing it away.
	envelope := map[string]any{"status": resp.StatusCode}
	if len(raw) > 0 && json.Valid(raw) {
		envelope["body"] = json.RawMessage(raw)
	} else {
		envelope["body"] = string(raw)
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Available reports whether a web view can be created on this system.
//
// The GUI is an optional extra: on a headless Linux box there is no WebKitGTK
// and no window to open, and the caller should fall back to the CLI with a clear
// message rather than a crash.
func Available() bool {
	switch {
	case os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "":
		return true
	case os.Getenv("SESSIONNAME") != "": // Windows console session
		return true
	default:
		// Windows and macOS always have a window server when a user is logged
		// in; only X11/Wayland needs the environment check above.
		return os.PathSeparator == '\\'
	}
}
