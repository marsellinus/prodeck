//go:build linux

package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// XTest event type numbers used by FakeInput, straight from the X11 protocol.
const (
	xKeyPress      = 2
	xKeyRelease    = 3
	xButtonPress   = 4
	xButtonRelease = 5
	xMotionNotify  = 6
)

// FakeInput motion detail: the protocol reuses the detail field of
// MotionNotify to say whether the coordinates are absolute or a delta.
const (
	motionAbsolute = 0
	motionRelative = 1
)

// Rows of the X11 modifier map (GetModifierMapping).
const (
	modRowShift = 0
	modRowCtrl  = 2
	modRowAlt   = 3
	modRowSuper = 6
)

// Mouse button numbers in the X11 core protocol.
const (
	xButtonLeft   = 1
	xButtonMiddle = 2
	xButtonRight  = 3
	xWheelUp      = 4
	xWheelDown    = 5
	xWheelLeft    = 6
	xWheelRight   = 7
)

// unicodeKeysymBase is the keysym space that maps one-to-one onto Unicode code
// points; it is how a character with no dedicated keysym is looked up.
const unicodeKeysymBase = 0x01000000

// maxTextRunes caps one Text call. A paste-sized string belongs in a command,
// and an unbounded loop would hold the injector for as long as the client likes.
const maxTextRunes = 4096

// absInt is the magnitude of an int, used to turn a scroll direction into a
// number of wheel notches.
func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// xKeycode is a resolved key: which keycode to press and which shift level of
// that key produces the wanted keysym.
type xKeycode struct {
	code  xproto.Keycode
	level byte
}

// linuxInput injects events with the XTEST extension.
//
// XTEST is used rather than uinput because it delivers events through the X
// server's own input pipeline: they are indistinguishable from a real keyboard
// to the focused client, and they need no device permissions beyond access to
// the user's display.
type linuxInput struct {
	mu   sync.Mutex
	conn *xgb.Conn

	root   xproto.Window
	width  uint16
	height uint16

	// keymap is built from the server's own keyboard mapping, so a key press
	// produces the character the user's layout actually has there instead of
	// what a hard-coded US table assumes.
	keymap    map[xproto.Keysym]xKeycode
	shiftCode xproto.Keycode
	ctrlCode  xproto.Keycode
	altCode   xproto.Keycode
	superCode xproto.Keycode
}

func newLinuxInput() *linuxInput { return &linuxInput{} }

// ensure connects on first use.
func (li *linuxInput) ensure() error {
	if li.conn != nil {
		return nil
	}
	return li.connect()
}

// connect opens the display and loads the keyboard mapping.
func (li *linuxInput) connect() error {
	if os.Getenv("DISPLAY") == "" {
		return &PermissionError{
			Op:     "connecting to the X server",
			Reason: "DISPLAY is not set, so there is no display for the agent to type into",
			Hint:   "start the host from inside the user's graphical session (for example from a terminal in the desktop), not from a system service or an SSH login without X forwarding",
		}
	}
	conn, err := xgb.NewConn()
	if err != nil {
		return &PermissionError{
			Op:     "connecting to the X server",
			Reason: err.Error(),
			Hint:   "the agent must run inside the user's graphical session, with DISPLAY pointing at a display the user is logged into",
		}
	}
	if err := xtest.Init(conn); err != nil {
		conn.Close()
		return &PermissionError{
			Op:     "enabling the XTEST extension",
			Reason: err.Error(),
			Hint:   "the X server must have the XTEST extension enabled; most desktop servers do, but some kiosk and remote configurations disable it",
		}
	}
	li.conn = conn
	if err := li.loadMaps(); err != nil {
		conn.Close()
		li.conn = nil
		return err
	}
	return nil
}

// loadMaps reads the screen geometry, the keyboard mapping and the modifier
// mapping. They are re-read on every reconnect because an X server that
// restarted may have a different layout.
func (li *linuxInput) loadMaps() error {
	setup := xproto.Setup(li.conn)
	if setup == nil {
		return errors.New("platform: the X server returned no setup information")
	}
	screen := setup.DefaultScreen(li.conn)
	if screen == nil {
		return errors.New("platform: the X server has no default screen")
	}
	li.root = screen.Root
	li.width = screen.WidthInPixels
	li.height = screen.HeightInPixels

	count := byte(setup.MaxKeycode-setup.MinKeycode) + 1
	reply, err := xproto.GetKeyboardMapping(li.conn, setup.MinKeycode, count).Reply()
	if err != nil {
		return fmt.Errorf("platform: reading the keyboard mapping: %w", err)
	}
	per := int(reply.KeysymsPerKeycode)
	if per == 0 {
		return errors.New("platform: the X server returned an empty keyboard mapping")
	}
	keymap := make(map[xproto.Keysym]xKeycode, len(reply.Keysyms))
	for i, sym := range reply.Keysyms {
		if sym == 0 {
			continue
		}
		if _, exists := keymap[sym]; exists {
			continue // first match wins: level 0 is preferred over shifted
		}
		keymap[sym] = xKeycode{
			code:  xproto.Keycode(int(setup.MinKeycode) + i/per),
			level: byte(i % per),
		}
	}
	li.keymap = keymap

	modReply, err := xproto.GetModifierMapping(li.conn).Reply()
	if err != nil {
		return fmt.Errorf("platform: reading the modifier mapping: %w", err)
	}
	perMod := int(modReply.KeycodesPerModifier)
	pick := func(row int) xproto.Keycode {
		for i := range perMod {
			if idx := row*perMod + i; idx < len(modReply.Keycodes) && modReply.Keycodes[idx] != 0 {
				return modReply.Keycodes[idx]
			}
		}
		return 0
	}
	li.shiftCode = pick(modRowShift)
	li.ctrlCode = pick(modRowCtrl)
	li.altCode = pick(modRowAlt)
	li.superCode = pick(modRowSuper)
	return nil
}

// closeConn drops the connection so the next call reconnects.
func (li *linuxInput) closeConn() {
	if li.conn != nil {
		li.conn.Close()
		li.conn = nil
	}
}

// isConnError reports whether err means the X server went away, which is worth
// one reconnect: a restarted session is the common cause and the layout is
// otherwise identical.
func isConnError(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}

// withConn runs fn against a live connection, reconnecting once if the server
// dropped it.
func (li *linuxInput) withConn(fn func() error) error {
	if err := li.ensure(); err != nil {
		return err
	}
	err := fn()
	if err == nil || !isConnError(err) {
		return err
	}
	li.closeConn()
	if err := li.connect(); err != nil {
		return err
	}
	return fn()
}

// lookup resolves a keysym to a keycode and shift level.
//
// The mapping is consulted first, because it is the layout's own answer. Two
// fallbacks cover the cases it does not: a layout that lists only the unshifted
// level of a letter (many non-US layouts rely on Caps Lock for the capital) is
// handled by looking the lowercase key up and forcing Shift; a character with
// no dedicated keysym at all is looked for in the Unicode keysym space, which
// is where the server puts the symbols of a rich layout.
func (li *linuxInput) lookup(sym uint32) (xKeycode, error) {
	if kc, ok := li.keymap[xproto.Keysym(sym)]; ok {
		return kc, nil
	}
	if sym >= 'A' && sym <= 'Z' {
		if kc, ok := li.keymap[xproto.Keysym(sym+0x20)]; ok {
			kc.level = 1 // hold Shift: the lowercase key is the one that exists
			return kc, nil
		}
	}
	if sym > 0x7f {
		if kc, ok := li.keymap[xproto.Keysym(unicodeKeysymBase+sym)]; ok {
			return kc, nil
		}
	}
	return xKeycode{}, fmt.Errorf("platform: the X keyboard layout has no key for U+%04X", sym)
}

// fakeKey sends a bare key event. Callers hold li.mu.
func (li *linuxInput) fakeKey(typ byte, code xproto.Keycode) error {
	return xtest.FakeInput(li.conn, typ, byte(code), xproto.TimeCurrentTime, 0, 0, 0, 0).Check()
}

// sendKey sends one key, wrapping it in Shift when its keysym lives on the
// shifted level of the physical key.
func (li *linuxInput) sendKey(kc xKeycode, mode KeyMode) error {
	shift := kc.level == 1 && li.shiftCode != 0
	switch mode {
	case KeyDown:
		if shift {
			if err := li.fakeKey(xKeyPress, li.shiftCode); err != nil {
				return err
			}
		}
		return li.fakeKey(xKeyPress, kc.code)
	case KeyUp:
		if err := li.fakeKey(xKeyRelease, kc.code); err != nil {
			return err
		}
		if shift {
			return li.fakeKey(xKeyRelease, li.shiftCode)
		}
		return nil
	default:
		if shift {
			if err := li.fakeKey(xKeyPress, li.shiftCode); err != nil {
				return err
			}
		}
		if err := li.fakeKey(xKeyPress, kc.code); err != nil {
			return err
		}
		if err := li.fakeKey(xKeyRelease, kc.code); err != nil {
			return err
		}
		if shift {
			return li.fakeKey(xKeyRelease, li.shiftCode)
		}
		return nil
	}
}

func (li *linuxInput) Key(ctx context.Context, key Key, mode KeyMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	def, err := LookupKey(string(key))
	if err != nil {
		return err
	}
	if def.X11 == 0 {
		return fmt.Errorf("platform: key %q has no X11 keysym", def.Name)
	}
	li.mu.Lock()
	defer li.mu.Unlock()
	return li.withConn(func() error {
		kc, err := li.lookup(def.X11)
		if err != nil {
			return err
		}
		return li.sendKey(kc, mode)
	})
}

func (li *linuxInput) Shortcut(ctx context.Context, keys []Key, mode KeyMode) error {
	if len(keys) == 0 {
		return fmt.Errorf("platform: shortcut needs at least one key")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	defs := make([]KeyDef, 0, len(keys))
	for _, k := range keys {
		def, err := LookupKey(string(k))
		if err != nil {
			return err
		}
		if def.X11 == 0 {
			return fmt.Errorf("platform: key %q has no X11 keysym", def.Name)
		}
		defs = append(defs, def)
	}

	li.mu.Lock()
	defer li.mu.Unlock()
	return li.withConn(func() error {
		resolved := make([]xKeycode, 0, len(defs))
		for _, def := range defs {
			kc, err := li.lookup(def.X11)
			if err != nil {
				return err
			}
			resolved = append(resolved, kc)
		}
		down := func(kc xKeycode) error {
			if kc.level == 1 && li.shiftCode != 0 {
				if err := li.fakeKey(xKeyPress, li.shiftCode); err != nil {
					return err
				}
			}
			return li.fakeKey(xKeyPress, kc.code)
		}
		up := func(kc xKeycode) error {
			if err := li.fakeKey(xKeyRelease, kc.code); err != nil {
				return err
			}
			if kc.level == 1 && li.shiftCode != 0 {
				return li.fakeKey(xKeyRelease, li.shiftCode)
			}
			return nil
		}
		switch mode {
		case KeyDown:
			for _, kc := range resolved {
				if err := down(kc); err != nil {
					return err
				}
			}
			return nil
		case KeyUp:
			// Release in reverse order, which is what a hand does and what
			// applications that watch key order expect.
			for i := len(resolved) - 1; i >= 0; i-- {
				if err := up(resolved[i]); err != nil {
					return err
				}
			}
			return nil
		default:
			for _, kc := range resolved {
				if err := down(kc); err != nil {
					return err
				}
			}
			for i := len(resolved) - 1; i >= 0; i-- {
				if err := up(resolved[i]); err != nil {
					return err
				}
			}
			return nil
		}
	})
}

func (li *linuxInput) Text(ctx context.Context, text string, interval time.Duration) error {
	if text == "" {
		return fmt.Errorf("platform: text must not be empty")
	}
	runes := []rune(text)
	if len(runes) > maxTextRunes {
		return fmt.Errorf("platform: text of %d characters exceeds the limit of %d", len(runes), maxTextRunes)
	}
	li.mu.Lock()
	defer li.mu.Unlock()
	err := li.withConn(func() error {
		for i, r := range runes {
			if err := ctx.Err(); err != nil {
				return err
			}
			kc, err := li.lookup(uint32(r))
			if err != nil {
				return err
			}
			if err := li.sendKey(kc, KeyPress); err != nil {
				return err
			}
			if interval > 0 && i < len(runes)-1 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(interval):
				}
			}
		}
		return nil
	})
	return err
}

func (li *linuxInput) MouseClick(ctx context.Context, btn MouseButton, count int) error {
	if count < 1 {
		count = 1
	}
	if count > 10 {
		return fmt.Errorf("platform: click count %d is above the limit of 10", count)
	}
	var code byte
	switch btn {
	case MouseLeft, "":
		code = xButtonLeft
	case MouseRight:
		code = xButtonRight
	case MouseMiddle:
		code = xButtonMiddle
	default:
		return fmt.Errorf("platform: mouse button %q must be left, right or middle", btn)
	}
	li.mu.Lock()
	defer li.mu.Unlock()
	return li.withConn(func() error {
		for i := range count {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := xtest.FakeInput(li.conn, xButtonPress, code, xproto.TimeCurrentTime, 0, 0, 0, 0).Check(); err != nil {
				return err
			}
			if err := xtest.FakeInput(li.conn, xButtonRelease, code, xproto.TimeCurrentTime, 0, 0, 0, 0).Check(); err != nil {
				return err
			}
			if i < count-1 {
				// A double click must land inside the desktop's double-click
				// time; 40 ms is well within it on every default configuration.
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(40 * time.Millisecond):
				}
			}
		}
		return nil
	})
}

func (li *linuxInput) MouseMove(ctx context.Context, dx, dy int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	li.mu.Lock()
	defer li.mu.Unlock()
	return li.withConn(func() error {
		return xtest.FakeInput(li.conn, xMotionNotify, motionRelative, xproto.TimeCurrentTime,
			li.root, int16(dx), int16(dy), 0).Check()
	})
}

func (li *linuxInput) MouseMoveAbsolute(ctx context.Context, x, y float64) error {
	if x < 0 || x > 1 || y < 0 || y > 1 {
		return fmt.Errorf("platform: absolute position %.3f,%.3f must be within 0..1 on both axes", x, y)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	li.mu.Lock()
	defer li.mu.Unlock()
	return li.withConn(func() error {
		px := int16(x*float64(li.width) + 0.5)
		py := int16(y*float64(li.height) + 0.5)
		return xtest.FakeInput(li.conn, xMotionNotify, motionAbsolute, xproto.TimeCurrentTime,
			li.root, px, py, 0).Check()
	})
}

func (li *linuxInput) MouseScroll(ctx context.Context, dx, dy int) error {
	if dx == 0 && dy == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Positive dy scrolls up, which is button 4; positive dx scrolls right,
	// which is button 7.
	var codes []byte
	for range absInt(dy) {
		if dy > 0 {
			codes = append(codes, xWheelUp)
		} else {
			codes = append(codes, xWheelDown)
		}
	}
	for range absInt(dx) {
		if dx > 0 {
			codes = append(codes, xWheelRight)
		} else {
			codes = append(codes, xWheelLeft)
		}
	}
	li.mu.Lock()
	defer li.mu.Unlock()
	return li.withConn(func() error {
		for _, code := range codes {
			if err := xtest.FakeInput(li.conn, xButtonPress, code, xproto.TimeCurrentTime, 0, 0, 0, 0).Check(); err != nil {
				return err
			}
			if err := xtest.FakeInput(li.conn, xButtonRelease, code, xproto.TimeCurrentTime, 0, 0, 0, 0).Check(); err != nil {
				return err
			}
		}
		return nil
	})
}
