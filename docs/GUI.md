# The desktop control panel

`mobiledeck gui` opens a native window that shows the host's state, edits deck
layouts, and manages paired devices. It is a client of the running host: every
button calls the same loopback admin API the command line calls, so the two can
never disagree about what an operation does (docs/adr/0011-desktop-gui.md).

```sh
cd host
go build -o mobiledeck ./cmd/mobiledeck
./mobiledeck gui
```

The host starts inside the same process. Closing the window **leaves it running**
in the Windows notification area, so the deck on the phone keeps working while the
window is out of the way; `--no-tray` restores the older behaviour of stopping
with the window.

## The notification area

On Windows the panel puts an icon in the tray:

| Action | What happens |
|--------|--------------|
| Left click, double click | Brings the window back |
| Show the control panel | The same |
| Reachable at *address* | A label, not a button: the address a phone should use |
| Quit MobileDeck | Stops the host and removes the icon |

Closing the window hides it; it does not stop the host. That is the point of the
icon, and it is the difference between tidying your desktop and shutting the deck
down. The tray is not a second control surface: everything except show, address
and quit is in the panel.

A build with CGO disabled, and every non-Windows platform, has no notification
area. `mobiledeck gui` says so on start rather than leaving you to look for an
icon that will not appear, and closing the window stops the host as it always
did.

## Opening the panel in a browser

`GET /` serves the same panel to a browser, so the host can be controlled with no
window and no CGO:

```sh
./mobiledeck run
./mobiledeck token        # prints the key the page asks for
# then open http://127.0.0.1:8765/ and paste it
```

The admin API is loopback-only (docs/SECURITY.md), so this is a panel for the
machine that runs the host, not for the phone on the network. The page asks for
the token once and keeps it in the browser's local storage; `mobiledeck token`
prints it, and `mobiledeck token --json` prints it with the URL.

A client that is not a browser still gets the plain-text summary from the same
address, which is what `mobiledeck status` and `curl` read.

## Status

| Panel | What it does |
|-------|--------------|
| Board | The grid the phone shows. Press an empty square to add something, a filled one to change it, hover one and press ✕ to remove it. Drag a square onto another to swap them. |
| Connect a phone | Three numbered steps, every address with a copy button, and the pairing PIN in large type with a countdown |
| Sounds | Drop an audio file, press a sound to hear it on the host, and put one on a square |
| Phones | Each paired phone and, in words, what it is allowed to do |

The wording throughout is the wording a person would use. A square is a square,
not a cell; the options are not JSON unless you open Advanced; and every action
is named by its outcome ("Press keys", "Lock the computer") rather than by its
type. The full registry is still reachable, through "Something else".

## Editing a layout

The grid on the Board tab is the profile's actual grid, at its actual size. Press
an empty square to add something to it, or a filled one to edit it.

### Adding

An empty square is a `+`. Pressing it asks **"What should this square do?"**, and
answers are outcomes: Play a sound, Press keys, Type text, Open a program, Click
the mouse, Scroll, Do several things in a row, and so on. Whichever you pick, the
panel asks for what that action needs — the keys, the file, the address — in the
panel, not in a browser dialog, because a browser dialog cannot show the sounds
you have or the boards you made and some browsers suppress it entirely.

**Press keys** records the shortcut: click the box and press the keys. The
recorder takes the keystroke, so Ctrl+S records a shortcut instead of saving the
board, and Escape clears a wrong chord instead of closing the dialog. The names
the host uses are its own — `PAGEDOWN`, `BRACKETLEFT` — and they are listed under
"A key that is not on this keyboard" for a key you do not have.

**Do several things in a row** builds a sequence: add steps, add pauses, reorder
them with Up. Each step asks the same questions a square does. That is the macro
action, in words.

### Removing

Hover a square and press **✕** in its corner, or open it and use **Delete this
square** at the bottom. The confirmation names the square, because a board of
soundboard pads has several that look alike.

- **Label** is what the phone shows.
- **Icon** is an emoji, or a Material icon name when the text is plain ASCII and
  longer than two characters. `power_settings_new` becomes a Material icon; `🔥`
  stays an emoji.
- **Row, column and spans** place the button. A cell that is already taken is
  refused, with the id of the button that holds it, because two buttons in one
  cell would render on top of each other and the phone would have to pick a
  winner.

### Moving a button

**Drag a tile to move it.** Dropping it on an empty cell moves the button there;
dropping it on another button **swaps the two**, which is what you mean when you
drag one onto the other. The tile under the cursor is outlined green for a move
and amber for a swap, so the result is visible before you release.

The grid is saved as soon as you drop, so the phone updates immediately.

If the host refuses the move, the panel reloads the profile it actually holds and
says why. That matters: without it the panel would keep showing the refused
layout, and every later save would fail for the same reason, leaving the grid
unsaveable until the window was reopened.
- **What it does** is chosen from the host's own registry, with the scope it needs
  shown next to it. The list is read from the host, so it cannot offer something
  the host will not run.
- **Which keys** appears for a shortcut square and is a recorder, not a text box.
- **Options** is JSON, under a label that says so. It is filled in for you, and
  the example is the host's own, so it works without being edited.
- **How big it is** is Narrower/Wider/Shorter/Taller, with the numbers under
  Advanced. A telemetry square additionally needs a metric and a format such as
  `{value:.0f}%`.

**Save** writes the profile, validates it, reloads it, and tells every connected
phone to refresh. There is no separate apply step: press a button in the panel
and it appears on the phone.

A profile that fails validation is **not** written, and the error names the
offending field, for example:

```
profile: /pages/0/buttons/16/cell overlaps button "notify" at row 3 column 3
```

That is the point of the editor refusing rather than warning: a saved-but-broken
profile would leave the deck unloadable with no indication of which edit did it.

## The soundboard

A soundboard pad is a square whose action is `sound.play`, and it looks like one:
a filled coloured key with a waveform instead of a bordered tile, so a board of
sounds is visibly a different thing from a board of shortcuts.

The Sounds tab lists what is on this computer, plays a sound here so you can hear
it before binding it, and puts one on the first free square. A sound can also be
dragged from the list onto any square, including one that already has something
on it, which it replaces. Files may be dropped on the tab or copied straight into
the directory shown at the bottom of it.

## Adding something that is not in the catalogue

The editor offers what the host can run. To add a new capability:

1. Add the action to the host (see `CONTRIBUTING.md`, "Adding an action").
2. Add it to the catalogue table in `docs/PROTOCOL.md` §10.
3. Add an entry to the `docs` map in `host/internal/server/admin_profiles.go` so
   the editor can show its parameters and an example.

The panel needs no change: it renders whatever the host reports.

Plugins will supply their own actions the same way once the runtime lands in
Phase 3 (`docs/PLUGIN_DEVELOPMENT.md`); the editor will pick them up from the
same registry listing.

## Several phones

Each paired phone is a device with its own token and its own scope set. The
Devices tab is where those are managed: give a wall-mounted tablet only `media`,
and keep `scripts` and `system.power` for the phone you carry. See
`docs/MULTI_DEVICE.md`.

## Headless machines

There is no window on a server with no display, and `mobiledeck gui` says so and
suggests `mobiledeck run`. The command line covers everything the panel does; the
panel is a convenience, not a requirement.

On Linux the GUI needs WebKitGTK at build time:

```sh
sudo apt install libwebkit2gtk-4.1-dev
```
