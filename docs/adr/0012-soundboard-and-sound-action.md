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
   panel offers no others. On Windows that is WAV: `PlaySoundW` decodes WAV through
   the system codec and nothing else. Another format is refused by name, with the
   format in the message, rather than accepted and then silently ignored.

4. **Volume is honest or it is absent.** `PlaySoundW` has no volume parameter, so
   the Windows adapter reports `VolumeIgnored() bool` and the action's result says
   the level was not applied. Faking it would mean decoding and rescaling samples,
   which is a media framework, not a soundboard.

5. **Audio crosses the admin API as base64 JSON, never as bytes.** The desktop
   panel reaches the host through one bridge that returns the response body as a
   string, and the admin token never reaches JavaScript, so a raw `audio/wav`
   response would be decoded as text and corrupted. The endpoint returns
   `{"mime":…,"data":"<base64>"}` and the panel plays it from a data URL.

6. **A file a profile still plays cannot be deleted** (409), and the reference
   check descends into macros, so a sound used only inside a sequence still counts
   as used. The alternative is a board with a square that looks configured and
   produces nothing.

## Consequences

- **The host stays a single binary with no runtime dependency.** `PlaySoundW` is
  in `winmm.dll`, which every supported Windows ships; Linux and macOS use a CLI
  player that is already installed (`paplay`/`aplay`/`ffplay`, `afplay`).
- **Windows can only play WAV, and that is visible rather than surprising.** A
  user who drops an MP3 is told the format is not playable and why, at upload
  time, instead of pressing a silent button later.
- **The trade-off is real and is the price of the choice.** A soundboard that
  played every format would need a decoding library, which would mean either a
  CGO dependency or a large pure-Go decoder, and either one breaks the property
  that makes the host installable with `scp` and `chmod +x` (ADR-0009). A
  soundboard that plays WAV everywhere and everything else where the platform
  already can is the version that ships.
- **The panel cannot stream audio**, which is why the endpoint is JSON. It costs
  a third more bytes on the wire and caps a file at the bridge's 8 MiB read, which
  is why that is also the upload limit.
- **`sound.play` needs the `media` scope**, alongside the transport controls it
  has nothing to do with operationally. A separate scope was rejected: it would be
  a second permission to explain in the pairing screen for a capability that
  cannot do more harm than the one already there.
