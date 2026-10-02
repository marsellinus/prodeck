# ADR-0013: The window is closable, the host keeps running, and the panel is also a web page

Status: accepted (Milestone 3)

## Context

Two problems, both found by using the panel rather than by reading it.

**Closing the window stopped the host.** That was the original design and it is
wrong for the common case. A user tidies their desktop, and the deck their phone
is holding disappears with it. The wish "close this window" and the wish "stop
the host" are different wishes, and only one of them is expressed by a close
button.

**The panel needed a window.** The web view is a C library: CGO, WebView2 on
Windows, WebKitGTK on Linux, a desktop session. A headless machine has none of
those, so it had no panel at all, and the panel could not be opened from anything
but the machine it runs on.

## Decision

**The tray owns the lifecycle, and the panel is served as a page.**

1. `mobiledeck gui` puts an icon in the notification area
   (`github.com/energye/systray`) and **closing the window hides it** rather than
   destroying it. The Win32 window procedure is subclassed so `WM_CLOSE` becomes
   a hide; every other message is passed through unchanged. The host is only
   stopped by Quit in the tray menu, by `mobiledeck stop`, or by a signal.
   `--no-tray` restores stopping with the window, and it is what a build with no
   notification area does anyway.

2. **`GET /` serves the same panel to a browser.** The distinction is the
   `Accept` header, so `mobiledeck status` and `curl` keep getting the plain-text
   summary they were written against. The panel is embedded in a file with no
   build tag, so a `CGO_ENABLED=0` build serves it too.

3. **The page has a second bridge for the browser case.** In the window, the
   Go side injects one function and keeps the admin token to itself. In a browser
   there is no injection, so the page implements the same function against the
   admin API, with a token the user pastes once and the browser remembers.
   `mobiledeck token` prints it.

## Consequences

- **A closed window is not a stopped host.** The phone keeps working while the
  panel is out of the way, which is what a background agent should have done from
  the start.
- **The tray is not a second control surface.** It shows the window, shows the
  address, and quits. Everything else stays in the panel, so there is no second
  place for behaviour to drift.
- **The browser panel is a same-machine panel, and that is deliberate.** The
  admin API is loopback-only and emits no CORS header (ADR-0006,
  docs/SECURITY.md): the token can revoke devices and rewrite profiles. Serving
  the page to the LAN would hand that to anyone who can open a URL. The phone
  talks to the host over the paired WebSocket, never over the admin API.
- **A missing token and a wrong one both answer 401**, so the page says which it
  is and offers the command that fixes it, instead of showing an empty board for
  a reason the user cannot guess.
- **The tray costs one dependency.** `github.com/energye/systray` is pure Go plus
  `godbus` on Linux, so the single-binary property (ADR-0009) holds. It is
  Windows-and-CGO only, and the fallback is a no-op that reports itself, so
  nothing else has to know.
- **Hiding a window is Win32-specific.** Non-Windows builds keep the old
  behaviour and say so on start. Doing this properly on Linux and macOS is a
  separate change with its own tradeoffs, not something to guess at now.
