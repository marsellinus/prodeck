//go:build windows

package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// winmm is resolved lazily: a host that never plays a sound should not load the
// multimedia library at all. MCI is not wrapped by x/sys, so it is called
// through the DLL directly, exactly as windows_power.go calls LockWorkStation.
var (
	winmm                 = windows.NewLazySystemDLL("winmm.dll")
	procMCISendString     = winmm.NewProc("mciSendStringW")
	procMCIGetErrorString = winmm.NewProc("mciGetErrorStringW")
)

// mciDeviceType is the MCI device MCI is asked for by name.
//
// It is spelled out rather than left to MCI's extension sniffing because the
// device MCI picks for a .wav on its own is waveaudio, and waveaudio rejects
// `setaudio ... volume` with "the driver cannot recognize the specified
// command" (261). Asking for mpegvideo decodes WAV, MP3, WMA, MIDI and AVI
// through the DirectShow codecs and, unlike waveaudio, accepts a volume level.
// Without the explicit type, a profile that set a level on a WAV would fail
// with 261 even though the file plays perfectly, while the same level on an
// MP3 would work — a difference no user could explain, and one VolumeIgnored()
// could not honestly describe for both.
const mciDeviceType = "mpegvideo"

// mciUnsupportedExts are the containers the Windows codecs do not decode.
//
// MCI reports these as a generic 277 ("A problem occurred in initializing
// MCI"), which names nothing a user can act on, so they are refused here with
// the format in the message instead. The list is deliberately an explicit deny
// set rather than an allow set: anything else is handed to MCI and, if the
// machine cannot decode it, the user gets mciGetErrorStringW's own words.
var mciUnsupportedExts = map[string]bool{
	".ogg":  true,
	".flac": true,
	".m4a":  true,
	".aac":  true,
	".opus": true,
}

// mciSweepInterval is how often open devices are checked for having finished.
//
// A device that is never closed leaks a handle and an entry in MCI's table for
// the life of the process, so closing is not optional. The interval trades a
// little shutdown latency for a cheap check: a 100 ms tick costs one `status`
// call per sound still playing, and the longest a finished sound can linger is
// that tick.
const mciSweepInterval = 100 * time.Millisecond

// mciReadyTimeout bounds the wait for a freshly opened alias to become
// queryable. MCI registers an alias asynchronously, so a `status` issued in the
// same instant can still be told the device does not exist; the window is
// normally zero-length and this is the ceiling that keeps a pathological open
// from hanging the owner thread.
const mciReadyTimeout = 2 * time.Second

// windowsSound plays a file through MCI (mciSendStringW), the one audio API
// Windows exposes without COM or a media framework.
//
// The format split is deliberate: MCI decodes WAV, MP3, WMA, MIDI and AVI out
// of the box, so the formats people actually have play with no extra
// dependency, and the ones it cannot decode (ogg, flac, m4a, aac, opus) are
// refused by name rather than accepted and then silently ignored. A button that
// looks configured and produces no noise is the worst possible failure for a
// soundboard, so the refusal is explicit and names the format.
//
// WAV does NOT keep a separate PlaySoundW path, even though PlaySound starts a
// few milliseconds sooner because it has no device to open. PlaySound cannot
// set a level at all, and VolumeIgnored() is a property of the adapter rather
// than of one file, so keeping both paths would mean either claiming a level
// for a WAV that was never applied or disowning the level an MP3 really did
// get. One path that can do both is the honest one, and the open cost is tens
// of milliseconds against a press that already crossed a network.
type windowsSound struct {
	Unsupported
}

// SoundAvailable reports that MCI is always present. It is part of winmm.dll,
// which every supported Windows version ships, so there is no runtime probe to
// make.
func (windowsSound) SoundAvailable() bool { return true }

// VolumeIgnored reports that a requested level IS applied: MCI's setaudio sets
// a per-device level from 0 to 1000, which maps exactly onto the 0..100 the
// action takes. The action therefore reports the level it was given instead of
// disclaiming it.
func (windowsSound) VolumeIgnored() bool { return false }

func (windowsSound) PlayFile(ctx context.Context, path string, volume int, blocking bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("platform: %s cannot be played: %w", path, err)
	}
	if ext := strings.ToLower(filepath.Ext(path)); mciUnsupportedExts[ext] {
		return fmt.Errorf("%w: the Windows media codecs have no decoder for %s; convert the file to wav, mp3 or wma for a soundboard button",
			ErrUnsupported, ext)
	}

	dev, err := mci.open(path, volume)
	if err != nil {
		return err
	}
	if err := mci.play(dev); err != nil {
		mci.close(dev)
		return err
	}
	if !blocking {
		// The sweep closes the device when the sound ends. Waiting here would
		// hold an action slot for the length of the file and make a press feel
		// slow, which is the opposite of what a soundboard is for.
		return nil
	}

	// Blocking playback must still honour cancellation. The device is stopped
	// and closed on the way out, which is what silences it; the sweep is not
	// relied on because a cancelled action should go quiet immediately rather
	// than at the next tick.
	select {
	case <-dev.done:
		return nil
	case <-ctx.Done():
		mci.close(dev)
		return ctx.Err()
	}
}

// mciEngine serialises every MCI call onto a single OS thread.
//
// The thread affinity is not an optimisation, it is a correctness requirement:
// MCI keeps its table of open devices per thread, so an alias opened on one
// thread is unknown on another. Go goroutines migrate between threads at
// preemption points, which is why a naive implementation opens a device
// successfully and then gets "the specified device is not open or is not
// recognized by MCI" (263) for the very next command — intermittently, and
// only under load. Pinning one owner goroutine and funnelling every command
// through it makes each alias stay on the thread that created it, and as a
// bonus MCI is never entered concurrently.
type mciEngine struct {
	once    sync.Once
	jobs    chan mciJob
	mu      sync.Mutex
	next    uint64
	devices map[string]*mciDevice
	sweep   *time.Ticker
}

// mciJob is one MCI command string and where its result goes.
type mciJob struct {
	text  string
	reply chan mciResult
}

// mciResult is the outcome of one MCI command: the numeric return code, the
// buffer MCI filled in on success, and the decoded message on failure.
type mciResult struct {
	code uint32
	text string
	err  string
}

// mciDevice is one open MCI device, identified by its alias.
type mciDevice struct {
	alias string
	// done is closed exactly once, when the device has been closed. A blocking
	// play waits on it, so the sweep can end the wait without the caller
	// polling and without a goroutine per sound.
	done chan struct{}
	// started is set once playback has begun. The sweep only closes devices
	// that have started, so it cannot close one between open and play.
	started bool
}

var mci = &mciEngine{devices: map[string]*mciDevice{}}

// start launches the owner goroutine on first use.
func (e *mciEngine) start() {
	e.once.Do(func() {
		e.jobs = make(chan mciJob)
		e.sweep = time.NewTicker(mciSweepInterval)
		go e.run()
	})
}

// run is the owner goroutine. It is locked to one OS thread for the life of the
// process so MCI's per-thread device table stays put.
func (e *mciEngine) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for {
		select {
		case job := <-e.jobs:
			job.reply <- e.exec(job.text)
		case <-e.sweep.C:
			// The sweep runs on the owner thread already, so it calls exec
			// directly: sending itself a job over e.jobs would deadlock on a
			// channel nobody else is reading.
			e.reap()
		}
	}
}

// do runs one MCI command on the owner thread and returns its result.
func (e *mciEngine) do(text string) mciResult {
	e.start()
	reply := make(chan mciResult, 1)
	e.jobs <- mciJob{text: text, reply: reply}
	return <-reply
}

// exec issues one MCI command. It runs only on the owner thread.
func (e *mciEngine) exec(text string) mciResult {
	cmd, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return mciResult{err: err.Error()}
	}
	buf := make([]uint16, 512)
	code, _, _ := procMCISendString.Call(
		uintptr(unsafe.Pointer(cmd)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0,
	)
	if code != 0 {
		return mciResult{code: uint32(code), err: mciErrorText(uint32(code))}
	}
	return mciResult{text: windows.UTF16ToString(buf)}
}

// mciErrorText turns an MCI return code into the message Windows has for it.
//
// A bare code is useless in a button result: 277 says nothing a user can act
// on, while the text that goes with it names the problem. If Windows has no
// string for the code the code itself is still reported rather than nothing.
func mciErrorText(code uint32) string {
	buf := make([]uint16, 512)
	ok, _, _ := procMCIGetErrorString.Call(
		uintptr(code),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if ok == 0 {
		return fmt.Sprintf("MCI error %d", code)
	}
	if text := strings.TrimSpace(windows.UTF16ToString(buf)); text != "" {
		return text
	}
	return fmt.Sprintf("MCI error %d", code)
}

// nextAlias returns an alias no other open device is using.
//
// The alias is what MCI keys a device by, and a second `open` on an alias that
// is already in use is refused outright with "the specified alias is already
// being used in this application" (289) — the first sound keeps playing, but
// the second press would fail. A soundboard must not do that: pressing a second
// pad has to start a second sound, so every open gets its own alias and every
// sound keeps its own device.
func (e *mciEngine) nextAlias() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.next++
	return "mobiledeck" + strconv.FormatUint(e.next, 10)
}

// open opens a device for path, applies the level, and returns it ready to
// play. volume is 0..100, or negative to leave the device at its default.
func (e *mciEngine) open(path string, volume int) (*mciDevice, error) {
	alias := e.nextAlias()
	dev := &mciDevice{alias: alias, done: make(chan struct{})}

	// The path is quoted because a sounds directory or file name may contain
	// spaces, which MCI would otherwise read as the start of the next argument.
	// A double quote cannot appear in a Windows path, so quoting is enough.
	if r := e.do(`open "` + path + `" type ` + mciDeviceType + ` alias ` + alias); r.code != 0 {
		return nil, fmt.Errorf("platform: Windows could not open %s for playback: %s", path, r.err)
	}
	if err := e.waitReady(alias); err != nil {
		e.do(`close ` + alias)
		return nil, fmt.Errorf("platform: Windows opened %s but it never became ready: %w", path, err)
	}
	if volume >= 0 {
		// MCI's level is 0..1000, so the action's 0..100 maps exactly by
		// multiplying by ten. The level is applied per device, so two sounds at
		// different volumes do not disturb each other.
		if r := e.do(`setaudio ` + alias + ` volume to ` + strconv.Itoa(volume*10)); r.code != 0 {
			e.do(`close ` + alias)
			return nil, fmt.Errorf("platform: Windows could not set the volume for %s to %d%%: %s", path, volume, r.err)
		}
	}

	e.mu.Lock()
	e.devices[alias] = dev
	e.mu.Unlock()
	return dev, nil
}

// waitReady waits for a freshly opened alias to answer a status query.
func (e *mciEngine) waitReady(alias string) error {
	deadline := time.Now().Add(mciReadyTimeout)
	for {
		if r := e.do(`status ` + alias + ` mode`); r.code == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s", mciReadyTimeout)
		}
		time.Sleep(time.Millisecond)
	}
}

// play starts playback and marks the device for the sweep to watch.
func (e *mciEngine) play(dev *mciDevice) error {
	// Playback is started without `wait`: a `wait` would block the owner thread
	// for the length of the file, so every other sound and every cancellation
	// would queue behind it. The sweep notices the end instead.
	if r := e.do(`play ` + dev.alias); r.code != 0 {
		return fmt.Errorf("platform: Windows could not start playback: %s", r.err)
	}
	e.mu.Lock()
	dev.started = true
	e.mu.Unlock()
	return nil
}

// close stops and closes a device. It is safe to call more than once and from
// the cancel path while the sweep is closing the same device.
func (e *mciEngine) close(dev *mciDevice) {
	e.do(`stop ` + dev.alias)
	e.do(`close ` + dev.alias)
	e.finish(dev.alias)
}

// reap closes devices whose sound has ended.
//
// This is the "close it when it ends" step for a non-blocking play, where
// nobody is waiting to do it. It is deliberately a periodic check rather than
// MCI's `play ... notify`: notify needs a window to post to, a message pump on
// that window's thread, and a Go callback trampoline behind the window
// procedure — three moving parts that fail quietly if any one is wrong. A
// ticker and a `status` call per live sound is a fraction of the code and
// cannot silently stop firing.
func (e *mciEngine) reap() {
	e.mu.Lock()
	devices := make([]*mciDevice, 0, len(e.devices))
	for _, dev := range e.devices {
		if dev.started {
			devices = append(devices, dev)
		}
	}
	e.mu.Unlock()

	for _, dev := range devices {
		r := e.exec(`status ` + dev.alias + ` mode`)
		// A device that no longer answers has gone away underneath us; closing
		// it is still the right move, and the close failure is ignored because
		// there is nothing left to close.
		if r.code == 0 && r.text == "playing" {
			continue
		}
		e.exec(`close ` + dev.alias)
		e.finish(dev.alias)
	}
}

// finish unregisters a device and releases anyone waiting on it.
func (e *mciEngine) finish(alias string) {
	e.mu.Lock()
	dev, ok := e.devices[alias]
	delete(e.devices, alias)
	e.mu.Unlock()
	if ok {
		close(dev.done)
	}
}
