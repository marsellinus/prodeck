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

The host starts inside the same process and stops when the window closes.

## Status

| Panel | What it does |
|-------|--------------|
| Status | Host id, address, TLS state, uptime, profiles, connected devices, and the action/scope catalogue for this machine |
| Layout | Create and edit profiles, pages and buttons; the grid is the real grid, so a cell that cannot render cannot be created |
| Devices | List, re-scope, disable, enable and revoke paired phones |
| Pair | Open a pairing window and show the PIN |

## Editing a layout

The grid on the Layout tab is the profile's actual grid, at its actual size.
Click an empty cell to create a button there, or a filled one to edit it.

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
- **Action** is chosen from the host's own registry, with the scope it needs
  shown next to it. The list is read from the host, so it cannot offer something
  the host will not run.
- **Parameters** are JSON, with a **Use example** button that fills in a working
  value for the chosen action.
- **State** is the tile behaviour: `momentary`, `toggle`, `radio`, `status`,
  `progress`, `counter`, `timer`, or `telemetry`. A telemetry button needs a
  metric and a format such as `{value:.0f}%`.

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
