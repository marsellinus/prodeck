# ADR-0012: Soundboard playback is the host's own, and the format it can play is the format it accepts

Status: accepted (Milestone 3)

## Context

A user asked for a soundboard: press a tile on the phone, a sound comes out of the
computer. That is a different thing from the media controls that already existed.
`media.play_pause` drives whatever player the user already has open, over MPRIS on
Linux or the media session on Windows. A soundboard has no player open. The host
itself must make the noise, immediately, from a file the user dropped somewhere.

The question is how to play a file on each platform, and what to do about the
formats and capabilities each one does not have.

## Decision

**A separate `Sound` capability, a separate `sound.play` action, and a directory
of files the host owns.**

1. `platform.Sound` is distinct from `platform.Media`, because the two are not
   related: one controls a session, the other produces audio. The interface is
   `PlayFile(ctx, path, volume, blocking)` and `SoundAvailable()`. It is not named
   `Play`/`Available` because `Media` and `Metrics` already declare those names on
   the shared `Unsupported` type, and two methods with one name and different
   signatures do not compile.

2. **Files live in one directory** (`sounds_dir`, default `<config>/sounds`) and a
   profile refers to a file by bare name. The name is validated, then both the
   directory and the resolved path go through `EvalSymlinks` and containment is
   checked again, so a symlink planted in the directory cannot walk out of it.

3. **The accepted formats are what the platform can actually play**, and the
   panel offers no others. On Windows that is WAV, MP3 and WMA, which MCI
   (`mciSendStringW` in `winmm.dll`) decodes through the DirectShow codecs the
   OS already ships. The formats the Windows codecs have no decoder for — ogg,
   flac, m4a, aac and opus — are refused by name, with the format in the
   message, rather than accepted and then silently ignored.

4. **Volume is honest or it is absent.** MCI exposes a per-device level
   (`setaudio <alias> volume to 0..1000`), so the Windows adapter applies the
   requested level and reports it as applied. The level is stored in eight bits,
   so a request for 70% reads back as 707 of 1000; that quantisation is MCI's
   and is not something the host can avoid. A platform with no level control
   still reports `VolumeIgnored() bool` and the action's result says the level
   was not applied. Faking a level by decoding and rescaling samples would mean
   a media framework, not a soundboard.

5. **Each press opens its own MCI device**, so a second pad does not cut the
   first one off. MCI keys devices by alias, and the adapter gives every open a
   fresh alias; a device is closed when its sound ends. The close is driven by a
   periodic sweep rather than MCI's `play ... notify`, which would need a window,
   a message pump and a Go callback behind its window procedure — three moving
   parts that fail quietly if any one is wrong.

6. **Audio crosses the admin API as base64 JSON, never as bytes.** The desktop
   panel reaches the host through one bridge that returns the response body as a
   string, and the admin token never reaches JavaScript, so a raw `audio/wav`
   response would be decoded as text and corrupted. The endpoint returns
   `{"mime":…,"data":"<base64>"}` and the panel plays it from a data URL.

7. **A file a profile still plays cannot be deleted** (409), and the reference
   check descends into macros, so a sound used only inside a sequence still counts
   as used. The alternative is a board with a square that looks configured and
   produces nothing.

## Consequences

- **The host stays a single binary with no runtime dependency.** MCI is
  `mciSendStringW` in `winmm.dll`, which every supported Windows ships, and the
  codecs it drives are the ones the OS already installs; Linux and macOS use a
  CLI player that is already installed (`paplay`/`aplay`/`ffplay`, `afplay`).
- **Windows plays what people actually have, and the rest is visible rather
  than surprising.** MP3, WAV, WMA and MIDI play; a file in ogg, flac, m4a, aac
  or opus is refused by name, with the format in the message, at press time
  instead of becoming a silent button. Refusing at upload time as well would
  mean the host rejecting a file that another host — or a future Windows —
  could play, so the refusal stays at the point where the answer is known.
- **MCI's device table is per-thread, and that shapes the implementation.** An
  alias opened on one OS thread is unknown on another, and Go goroutines
  migrate between threads at preemption points, so the naive version opens a
  device successfully and then gets `MCIERR_DEVICE_NOT_OPEN` for the very next
  command — intermittently, and only under load. Every MCI call therefore goes
  through one owner goroutine pinned to one OS thread. This is not an
  optimisation; without it the feature is unreliable.
- **A blocking press reports through the two-phase reply.** A blocking
  `sound.play` runs for the length of the file, which outlives the server's
  1500 ms synchronous window, so the client gets an `accepted` ack and the
  outcome as `event.action.finished`. That is the existing behaviour for any
  slow action, not something sound introduces.
- **The trade-off is real and is the price of the choice.** A soundboard that
  played every format would need a decoding library, which would mean either a
  CGO dependency or a large pure-Go decoder, and either one breaks the property
  that makes the host installable with `scp` and `chmod +x` (ADR-0009). A
  soundboard that plays what the OS already decodes is the version that ships.
- **`Result.Detail` is not sent to the client.** The action builds a detail
  line that names the volume it applied (or admits it could not), but the
  protocol's `action.result` payload has no field for it, so a client sees only
  `ok`. That is pre-existing and applies to every action, not just this one;
  fixing it is a protocol change, not a soundboard change.
- **The panel cannot stream audio**, which is why the endpoint is JSON. It costs
  a third more bytes on the wire and caps a file at the bridge's 8 MiB read, which
  is why that is also the upload limit.
- **`sound.play` needs the `media` scope**, alongside the transport controls it
  has nothing to do with operationally. A separate scope was rejected: it would be
  a second permission to explain in the pairing screen for a capability that
  cannot do more harm than the one already there.
