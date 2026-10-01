//go:build windows

// Package-level implementation of the platform contracts for Windows.
//
// Input injection uses SendInput, which is the documented, session-aware API:
// it works in the interactive session that the agent runs in and it respects
// the same input queue as a physical keyboard, so it is indistinguishable from
// real hardware to the receiving application.
package platform

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SendInput flag constants (winuser.h).
const (
	keyeventfExtendedKey = 0x0001
	keyeventfKeyUp       = 0x0002
	keyeventfUnicode     = 0x0004

	mouseeventfMove        = 0x0001
	mouseeventfLeftDown    = 0x0002
	mouseeventfLeftUp      = 0x0004
	mouseeventfRightDown   = 0x0008
	mouseeventfRightUp     = 0x0010
	mouseeventfMiddleDown  = 0x0020
	mouseeventfMiddleUp    = 0x0040
	mouseeventfWheel       = 0x0800
	mouseeventfHWheel      = 0x1000
	mouseeventfVirtualDesk = 0x4000
	mouseeventfAbsolute    = 0x8000

	wheelDelta = 120

	inputMouse    = 0
	inputKeyboard = 1

	// System metric indices used for absolute pointer positioning.
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

// The Windows INPUT structure is a tagged union, which Go cannot express
// directly.
//
// The union is therefore raw storage sized to the largest member, and the two
// members are written through typed accessors. Hand-computing field offsets into
// a differently-shaped struct is the trap here: MOUSEINPUT and KEYBDINPUT do not
// share a layout (KEYBDINPUT's dwFlags sits at offset 4, MOUSEINPUT's at 12), so
// writing a keyboard event through mouse-shaped fields silently puts the flags in
// the wrong place. The accessors make that mistake impossible, and the size
// assertion below keeps the storage honest.

// keybdInput mirrors KEYBDINPUT (winuser.h). Only used as a typed view over the
// union storage.
type keybdInput struct {
	Vk        uint16
	Scan      uint16
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

// mouseInput mirrors MOUSEINPUT (winuser.h). It is the larger member, so it
// determines the union size.
type mouseInput struct {
	Dx        int32
	Dy        int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

// input mirrors INPUT: a type tag, padding to the union's alignment, then the
// union storage.
type input struct {
	Typ uint32
	// Padding on 64-bit builds, where the union must start at offset 8. On
	// 32-bit builds the union starts at offset 4 and this field keeps the layout
	// correct there too, because uintptr is 4 bytes wide.
	_ uint32
	// U is the union storage, sized to its largest member.
	U [unsafe.Sizeof(mouseInput{})]byte
}

// asKeyboard views the union storage as a keyboard event.
func (in *input) asKeyboard() *keybdInput {
	return (*keybdInput)(unsafe.Pointer(&in.U[0]))
}

// asMouse views the union storage as a mouse event.
func (in *input) asMouse() *mouseInput {
	return (*mouseInput)(unsafe.Pointer(&in.U[0]))
}

// newKeyEvent builds a keyboard INPUT with the given virtual key, scan code and
// flags.
func newKeyEvent(vk, scan uint16, flags uint32) input {
	in := input{Typ: inputKeyboard}
	kb := in.asKeyboard()
	kb.Vk = vk
	kb.Scan = scan
	kb.Flags = flags
	return in
}

// newMouseEvent builds a mouse INPUT.
func newMouseEvent(dx, dy int32, data, flags uint32) input {
	in := input{Typ: inputMouse}
	mi := in.asMouse()
	mi.Dx = dx
	mi.Dy = dy
	mi.MouseData = data
	mi.Flags = flags
	return in
}

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procSendInput        = user32.NewProc("SendInput")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procSetCursorPos     = user32.NewProc("SetCursorPos")
	procBlockInput       = user32.NewProc("BlockInput")
)

func init() {
	// A wrong INPUT layout would silently inject garbage; assert the ABI at
	// start-up instead, where the failure is loud and immediate. The expected
	// size is 40 on 64-bit builds and 28 on 32-bit builds, per winuser.h:
	//   type (4) + padding (4) + MOUSEINPUT (32) = 40
	//   type (4) + padding (4) + MOUSEINPUT (20) = 28
	// 40 bytes on 64-bit builds and 28 on 32-bit ones, per winuser.h:
	//   type (4) + padding (4) + max(KEYBDINPUT, MOUSEINPUT)
	const headerSize = 8
	want := headerSize + int(unsafe.Sizeof(mouseInput{}))
	got := int(unsafe.Sizeof(input{}))
	if got != want {
		panic(fmt.Sprintf("platform: INPUT struct layout is %d bytes, expected %d", got, want))
	}
	// The union must start where Windows expects it, and the storage must be at
	// least as large as both members.
	if off := unsafe.Offsetof(input{}.U); off != headerSize {
		panic(fmt.Sprintf("platform: the INPUT union starts at offset %d, expected %d", off, headerSize))
	}
	if unsafe.Sizeof(keybdInput{}) > unsafe.Sizeof(mouseInput{}) {
		panic("platform: the union storage is smaller than KEYBDINPUT")
	}
	// The field offsets the accessors rely on, checked once at start-up.
	if unsafe.Offsetof(keybdInput{}.Flags) != 4 {
		panic("platform: KEYBDINPUT.dwFlags is not at offset 4")
	}
	if unsafe.Offsetof(mouseInput{}.Flags) != 12 {
		panic("platform: MOUSEINPUT.dwFlags is not at offset 12")
	}
}

type windowsInput struct {
	mu sync.Mutex // SendInput calls are serialised so a shortcut is atomic
}

func (wi *windowsInput) send(inputs []input) error {
	if len(inputs) == 0 {
		return nil
	}
	// Input is a shared, order-sensitive resource: interleaving two calls would
	// produce a shortcut made of half of each. The lock lives here rather than
	// on the interface method so that composite operations stay atomic.
	n, _, err := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(inputs[0]),
	)
	if n != uintptr(len(inputs)) {
		return &PermissionError{
			Op:     "sending input",
			Reason: err.Error(),
			Hint:   "SendInput fails when another process has blocked input (a UAC prompt on the secure desktop, or the agent running in a non-interactive session)",
		}
	}
	return nil
}

func (wi *windowsInput) Key(ctx context.Context, key Key, mode KeyMode) error {
	def, err := LookupKey(string(key))
	if err != nil {
		return err
	}
	if def.Windows == 0 {
		return fmt.Errorf("platform: key %q has no Windows virtual-key code", def.Name)
	}
	wi.mu.Lock()
	defer wi.mu.Unlock()
	return wi.send(keyEvents(def, mode))
}

func keyEvents(def KeyDef, mode KeyMode) []input {
	var flags uint32
	if def.WindowsExtended {
		flags |= keyeventfExtendedKey
	}
	down := newKeyEvent(def.Windows, 0, flags)
	up := newKeyEvent(def.Windows, 0, flags|keyeventfKeyUp)

	switch mode {
	case KeyDown:
		return []input{down}
	case KeyUp:
		return []input{up}
	default:
		return []input{down, up}
	}
}

func (wi *windowsInput) Shortcut(ctx context.Context, keys []Key, mode KeyMode) error {
	if len(keys) == 0 {
		return fmt.Errorf("platform: shortcut needs at least one key")
	}
	defs := make([]KeyDef, 0, len(keys))
	for _, k := range keys {
		def, err := LookupKey(string(k))
		if err != nil {
			return err
		}
		if def.Windows == 0 {
			return fmt.Errorf("platform: key %q has no Windows virtual-key code", def.Name)
		}
		defs = append(defs, def)
	}

	var events []input
	switch mode {
	case KeyDown:
		for _, d := range defs {
			events = append(events, keyEvents(d, KeyDown)...)
		}
	case KeyUp:
		// Release in reverse order, which is what a human hand does and what
		// applications that watch key order expect.
		for i := len(defs) - 1; i >= 0; i-- {
			events = append(events, keyEvents(defs[i], KeyUp)...)
		}
	default:
		for _, d := range defs {
			events = append(events, keyEvents(d, KeyDown)...)
		}
		for i := len(defs) - 1; i >= 0; i-- {
			events = append(events, keyEvents(defs[i], KeyUp)...)
		}
	}

	wi.mu.Lock()
	defer wi.mu.Unlock()
	return wi.send(events)
}

func (wi *windowsInput) Text(ctx context.Context, text string, interval time.Duration) error {
	if text == "" {
		return fmt.Errorf("platform: text must not be empty")
	}
	// Inject as UTF-16 code units with KEYEVENTF_UNICODE, which bypasses the
	// keyboard layout entirely: a deck can type an emoji or a non-Latin
	// character without the host having that layout installed.
	units := utf16.Encode([]rune(text))
	for i, u := range units {
		if err := ctx.Err(); err != nil {
			return err
		}
		// KEYEVENTF_UNICODE carries the UTF-16 code unit in wScan, and wVk must
		// be zero. The accessor makes the field assignment unambiguous, which
		// matters here: writing the unit into the virtual-key field instead
		// types whichever key shares that code rather than the intended
		// character.
		down := newKeyEvent(0, u, keyeventfUnicode)
		up := newKeyEvent(0, u, keyeventfUnicode|keyeventfKeyUp)

		wi.mu.Lock()
		err := wi.send([]input{down, up})
		wi.mu.Unlock()
		if err != nil {
			return err
		}
		if interval > 0 && i < len(units)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
	}
	return nil
}

func (wi *windowsInput) MouseClick(ctx context.Context, btn MouseButton, count int) error {
	if count < 1 {
		count = 1
	}
	if count > 10 {
		return fmt.Errorf("platform: click count %d is above the limit of 10", count)
	}
	var downFlag, upFlag uint32
	switch btn {
	case MouseLeft:
		downFlag, upFlag = mouseeventfLeftDown, mouseeventfLeftUp
	case MouseRight:
		downFlag, upFlag = mouseeventfRightDown, mouseeventfRightUp
	case MouseMiddle:
		downFlag, upFlag = mouseeventfMiddleDown, mouseeventfMiddleUp
	default:
		return fmt.Errorf("platform: mouse button %q must be left, right or middle", btn)
	}

	for i := range count {
		if err := ctx.Err(); err != nil {
			return err
		}
		down := newMouseEvent(0, 0, 0, downFlag)
		up := newMouseEvent(0, 0, 0, upFlag)

		wi.mu.Lock()
		err := wi.send([]input{down, up})
		wi.mu.Unlock()
		if err != nil {
			return err
		}
		if i < count-1 {
			// A double click must land inside the OS double-click time; 40 ms
			// is well within it on every default configuration.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(40 * time.Millisecond):
			}
		}
	}
	return nil
}

func (wi *windowsInput) MouseMove(ctx context.Context, dx, dy int) error {
	mv := newMouseEvent(int32(dx), int32(dy), 0, mouseeventfMove)
	wi.mu.Lock()
	defer wi.mu.Unlock()
	return wi.send([]input{mv})
}

func (wi *windowsInput) MouseMoveAbsolute(ctx context.Context, x, y float64) error {
	if x < 0 || x > 1 || y < 0 || y > 1 {
		return fmt.Errorf("platform: absolute position %.3f,%.3f must be within 0..1 on both axes", x, y)
	}
	// Normalised absolute coordinates address the virtual desktop, so a deck
	// works unchanged on a multi-monitor setup.
	ax := int32(x*65535 + 0.5)
	ay := int32(y*65535 + 0.5)
	mv := newMouseEvent(ax, ay, 0, mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk)
	wi.mu.Lock()
	defer wi.mu.Unlock()
	return wi.send([]input{mv})
}

func (wi *windowsInput) MouseScroll(ctx context.Context, dx, dy int) error {
	if dx == 0 && dy == 0 {
		return nil
	}
	var events []input
	if dy != 0 {
		events = append(events, newMouseEvent(0, 0, uint32(int32(dy*wheelDelta)), mouseeventfWheel))
	}
	if dx != 0 {
		events = append(events, newMouseEvent(0, 0, uint32(int32(dx*wheelDelta)), mouseeventfHWheel))
	}
	wi.mu.Lock()
	defer wi.mu.Unlock()
	return wi.send(events)
}

// ScreenSize returns the primary screen size in pixels, used by the CLI's
// diagnostics output.
func ScreenSize() (int, int, error) {
	w, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	return int(w), int(h), nil
}

// New builds the Windows platform adapters.
func New() *Platform {
	return &Platform{
		OSName:   "windows",
		Input:    &windowsInput{},
		Launcher: windowsLauncher{},
		Shell:    windowsShell{},
		Media:    windowsMedia{input: &windowsInput{}},
		Power:    windowsPower{},
		Metrics:  newWindowsMetrics(),
	}
}

// guard against the runtime being built for an architecture whose INPUT layout
// we have not reasoned about.
func init() {
	switch runtime.GOARCH {
	case "amd64", "arm64", "386", "arm":
	default:
		panic("platform: Unsupported architecture " + runtime.GOARCH)
	}
}
