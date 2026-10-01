# ADR-0011: Desktop control panel as a native window with an embedded web view

Status: accepted (Milestone 2)

## Context

The host is usable from the command line and from profiles edited by hand. That
is enough for the author and not enough for anyone else: a layout is a grid of
rectangles, and asking a user to write JSON to place a button is the single
largest barrier in the project. A GUI is needed, and it has to do three things:
show the host's state, edit a layout, and manage paired devices.

The question is what to build it with. The host is a single Go binary with no
runtime dependency, and that property is load-bearing (ADR-0009): it is why
`scp` and `chmod +x` is the whole installation procedure.

## Decision

A **native window wrapping an embedded web view**, driven by Go through
`github.com/webview/webview_go` (WebView2 on Windows, WebKitGTK on Linux,
WKWebView on macOS). The panel itself is one HTML file embedded in the binary
with `go:embed`.

## Consequences

- **One binary stays true.** The web view is a system component on every
  supported platform, so nothing is shipped with the app and nothing has to be
  installed. `go:embed` means the panel is not even a file on disk.
- **The panel is a client of the host, not a second implementation.** Every
  action it takes goes through the same loopback admin API the CLI uses. There is
  one implementation of "revoke a device" and one of "save a profile", so the GUI
  cannot drift from the command line, and the CLI keeps working for anyone who
  prefers it.
- **The admin token never reaches JavaScript.** The web view is given exactly one
  bound function, which proxies a request and attaches the bearer token on the Go
  side. Handing the token to the page would put a credential in reach of any
  injected script, and the token can rewrite profiles and revoke devices.
- **HTML for the layout editor is the right shape.** A layout editor is a grid of
  rectangles plus a form; that is what a browser is good at, and doing it in a
  native toolkit would mean writing a grid, a form and a renderer by hand for no
  benefit.
- **Cost: WebKitGTK on Linux.** `libwebkit2gtk-4.1-dev` is a build dependency for
  the `gui` command on Linux. The host binary still builds and runs without it —
  the GUI is behind its own file and only `mobiledeck gui` needs it — but a
  from-source Linux build that wants the panel must install it. Documented in
  DEVELOPMENT.md.
- **Cost: CGO.** `webview_go` needs CGO, so the GUI build is not a static
  cross-compiled binary. `CGO_ENABLED=0` builds every other command, and the
  release packaging builds the GUI per platform. Accepted: a window is inherently
  platform-specific.
- **Rejected: a native toolkit (Fyne, Gio, Wails).** Fyne and Gio draw their own
  widgets, which means reimplementing a text field, a table and a grid, and
  shipping fonts and a renderer. Wails adds a CLI, a build step and a JS
  toolchain to the repository.
- **Rejected: a local web server plus a browser tab.** It works with zero new
  dependencies, but it needs a token in the URL or a cookie, and it opens the
  admin API to whatever else can reach that port. The window keeps the surface on
  loopback and the credential in the process.

## The editor's design rule

Each editor endpoint edits **one** thing (a document, a page, a button) and
validates the **whole** document before writing it. A layout editor that can save
an invalid profile is worse than no editor, because the deck then fails to load
and the user has no way to tell which of their edits broke it. Every rejection
carries the JSON pointer of the offending field, so the panel can show the error
next to the input that caused it.

A save goes through the same path a hand edit does: validate, write atomically,
reload, and broadcast `event.profile.changed`. Connected phones therefore update
the moment the button is pressed in the panel, with no separate "apply" step.
