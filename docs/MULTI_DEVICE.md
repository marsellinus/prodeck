# Multiple devices

How several phones, tablets and laptops share one host. The host has supported
this at the protocol level from Milestone 1: each connection carries its own
device token, and scopes are per device. This document is the operator's guide.

Everything here is also reachable without a terminal: the control panel's
**Phones** tab lists each paired device with its scopes, turns one on or off, and
edits its scope set (see [`GUI.md`](GUI.md)). Revoking, renaming and the CLI
details below remain command-line operations.

---

## 1. How devices connect

A phone is paired once, with a PIN (`mobiledeck pair`). The host then creates a
**device record** containing:

- the device id the client generated (`android-<id>`, from the Android id), its
  name, platform and model;
- the **scope set** granted at pairing;
- `sha256(token)` — never the token itself (`docs/SECURITY.md` §6);
- first-seen and last-seen timestamps and the last IP.

Each device has its own record, its own token and its own scopes. There is no
shared credential and no per-host session limit of one: several phones may hold
a live WebSocket session at the same time, each authenticated separately.

The host tracks live sessions per device. `mobiledeck status` reports the count;
`mobiledeck devices` shows which records are currently connected.

---

## 2. Per-device scopes

Scopes decide what a device may run. The host checks them before an action
executes and rejects the whole request rather than partially executing it
(`docs/PROTOCOL.md` §9).

A newly paired device receives:

```
keyboard, mouse, media, apps, system.read, profiles.write
```

The two **high-risk** scopes, `scripts` and `system.power`, are never granted by
default. `scripts` runs `run_script`/`run_command` — a shell on the machine.
`system.power` shuts down, restarts, sleeps or locks it. A stolen token with
either is a serious loss, so the operator opts in explicitly.

List every scope and which are high-risk:

```sh
mobiledeck devices scopes-available
```

Read or replace one device's scopes:

```sh
# print the current set
mobiledeck devices scopes <id>

# replace it wholesale (the list is the new complete set, not a delta)
mobiledeck devices scopes <id> keyboard mouse scripts
```

The host validates the names and refuses an unknown one with the list of valid
scopes; the CLI checks locally first, so a typo is caught without a round trip.

---

## 3. The client limit

The host caps concurrent WebSocket sessions at `session.max_clients`, default
**16**. The cap is a config field, not a flag: there is no `--max-clients`
option in this build. Set it in `config.json` under the configuration directory:

```json
{
  "session": {
    "max_clients": 16
  }
}
```

Valid range is 1..512. When the cap is reached, the host refuses the **next**
connection with HTTP **503** and body
`{"error":"too_many_clients", ...}` **before** the WebSocket upgrade — so the
client never establishes a session, never sends `hello`, and never consumes a
token check. The refusal is logged with the limit and the peer IP.

The limit bounds slow-loris and hung sessions (`docs/SECURITY.md` T12); it is
not a licensing limit. Raise it only if you genuinely run more than 16 live
clients; each live session costs a goroutine and a socket.

---

## 4. One phone per role

Scopes make it practical to give different devices different jobs:

- A **wall-mounted tablet** with only `media`: it can play, pause and change
  volume, and nothing else. A token stolen from it cannot type.
  ```sh
  mobiledeck devices scopes wall-tablet media system.read
  ```
- A **personal phone** with everything you use day to day, `scripts` included
  only if you accept that it can run commands:
  ```sh
  mobiledeck devices scopes my-phone keyboard mouse media apps scripts system.read profiles.write
  ```
- A **guest phone** with the minimum: media only. Navigation actions
  (`deck.open_page`, `deck.back`, `deck.change_profile`, `deck.notify`) need no
  scope at all, so they work regardless; every other scope is withheld.
  ```sh
  mobiledeck devices scopes guest-phone media
  ```

The CLI requires at least one scope name; it cannot set an empty set. Grant the
narrowest non-empty set that does the job.

Grant the narrowest set that does the job. Adding a scope later is one command;
undoing the consequences of an over-broad token is not.

---

## 5. Revoking and disabling

Two different operations, with different effects. Choose deliberately.

### `devices revoke <id>`

Deletes the device record entirely. The token stops working immediately.

```sh
mobiledeck devices revoke android-7c1f
```

- A live session is closed with **4403** ("this device was revoked on the host").
- A later reconnect is an **unknown device** and is closed with **4401**.
- The record, its scopes and its history are gone. Re-pairing needs a fresh PIN
  and creates a new record with default scopes.

**Use it** for a lost or stolen phone, a decommissioned device, or any token you
no longer trust. This is the safe answer to "I am not sure who has that phone".

### `devices disable <id>`

Keeps the record and its scopes; refuses connections until re-enabled.

```sh
mobiledeck devices disable android-7c1f
mobiledeck devices enable android-7c1f
```

- A live session is closed with **4403**.
- A reconnect while disabled is also **4403** ("this device is disabled on the
  host").
- The record, token hash and scopes are retained, so `enable` restores the
  device exactly as it was, without re-pairing.

**Use it** to pause a device temporarily: a child's tablet during school hours,
a work phone while travelling, a device you are debugging. It is reversible and
cheap.

### How the client reacts

The client keys its behaviour off the close code (`docs/PROTOCOL.md` §2.5):

| Code | Client action |
|------|---------------|
| 4401 | Wipes the stored token and returns to pairing (with backoff). |
| 4403 | Does **not** reconnect automatically; shows an error banner and keeps the token. |

The close code depends on whether the device is currently connected:

| Operation | Live session | Later reconnect |
|-----------|--------------|-----------------|
| `revoke` | 4403 ("this device was revoked on the host") | 4401 ("authentication failed") |
| `disable` | 4403 ("this device was disabled on the host") | 4403 ("this device is disabled on the host") |

So the two operations look the same to a connected client at the moment you run
them, and only differ on the next connection attempt. A **revoked** device that
reconnects (reopen the app, or the reconnect action) is turned away with 4401 and
wipes its token, returning to pairing. A **disabled** device is turned away with
4403 on every attempt and keeps its token; once you `enable` it, reconnect from
the client (reopen the app, or the reconnect action) — it will not retry on its
own, because 4403 is deliberately non-retrying.

Note that the client's banner for 4403 reads "This device was revoked by the
host" for both operations; the host log and `audit.jsonl` record the real
distinction.

Use `revoke` when the token must die (lost phone). Use `disable` when you only
want to pause a device and restore it later without re-pairing.

---

## 6. Several hosts from one phone

The client binds a token to the host it was issued for. `TokenBundle` records
the `hostId` alongside the token, and the client refuses to present a token to a
host whose id does not match — this is what stops a captured token from being
replayed against a different host (`docs/SECURITY.md` T3). Cached profiles are
kept per host id on disk, so switching hosts does not lose the other host's
grid.

**One token per host is held, and several hosts may be paired at once.** The
store is keyed by host id, so pairing with a second machine does not disturb the
first: switching back to it reconnects with the token it already has, with no
second PIN. A `4401` from one host drops only that host's token, so a revocation
on one machine cannot sign the phone out of the others. `mobiledeck` on the phone
opens on the host it was last used with; the rest are one tap away in the host
list.

---

## 7. Worked example: two phones and a laptop

Goal: a personal phone with full control, a tablet that may only drive media,
and a laptop as the host. The host runs on `192.168.1.10:8765`.

**On the host**, start it and open a pairing window:

```sh
mobiledeck run
# in another terminal:
mobiledeck pair
```

The PIN is valid for 120 seconds and is single use, so pair one device per
`pair` invocation.

**Pair the personal phone.** In the app, pick the discovered host (or enter
`192.168.1.10:8765`), enter the PIN, and press Pair. The device id is printed by
`devices` afterwards; suppose it is `android-personal`. Leave its scopes as
granted, or widen them deliberately:

```sh
mobiledeck devices scopes android-personal keyboard mouse media apps scripts system.read profiles.write
```

**Pair the tablet.** Run `mobiledeck pair` again for a new PIN, pair on the
tablet (`android-tablet`), then narrow it to media only:

```sh
mobiledeck devices scopes android-tablet media system.read
```

**Confirm the result:**

```sh
mobiledeck devices
```

```
ID                  NAME            CONNECTED DISABLED SCOPES
android-personal    My Phone        yes       no       apps, keyboard, media, mouse, profiles.write, scripts, system.read
android-tablet      Wall Tablet     yes       no       media, system.read
```

Both sessions are live at once; each acts only within its own scopes. The
tablet pressing a `run_command` button gets `forbidden` because it does not hold
`scripts`, and the host never invokes the engine for it.

**Pause the tablet for the evening, then restore it:**

```sh
mobiledeck devices disable android-tablet
mobiledeck devices enable  android-tablet
```

**A phone is lost. Revoke it, permanently:**

```sh
mobiledeck devices revoke android-personal
```

Its session drops immediately; any reconnect fails as an unknown device and the
client returns to pairing. Pair a replacement with `mobiledeck pair`.

---

## 8. Auditing who did what

Every pairing attempt, auth decision, scope change and privileged action is
appended to `audit.jsonl` under the configuration directory, one JSON record per
line (`docs/SECURITY.md` §7). Each record carries the `device_id`, so per-device
history is a filter:

```sh
grep '"device_id":"android-tablet"' ~/.config/mobiledeck/audit.jsonl
```

Tokens and PINs never appear in the audit log or in the host log — only the
device id and a token fingerprint prefix.
