package platform

import (
	"fmt"
	"sort"
	"strings"
)

// Key is a canonical key name from the wire protocol, for example "CTRL", "A",
// "F5", "ENTER". Profiles always use these names; each OS implementation
// translates them into its own native code.
type Key string

// KeyDef maps one canonical name to the native code on each supported OS.
//
// The table lives in a build-tag-free file on purpose: a unit test then asserts
// that *every* key resolves on *every* OS, which is what keeps "porting is
// additive" true instead of decaying into a runtime surprise on one platform.
type KeyDef struct {
	Name string
	// Windows is the virtual-key code passed to SendInput.
	Windows uint16
	// WindowsExtended marks keys that need KEYEVENTF_EXTENDEDKEY. Getting this
	// wrong produces a key that logs correctly and does nothing in the target
	// application, so it is data rather than a per-call guess.
	WindowsExtended bool
	// X11 is the keysym value (X11/keysymdef.h). Keysyms rather than names
	// because the implementation resolves keysym -> keycode from the server's
	// own keyboard mapping, which works on any layout and needs no name table.
	X11 uint32
	// MacOS is the virtual key code (Carbon kVK_*). Empty means "not mapped
	// yet"; the darwin implementation reports those as Unsupported rather than
	// guessing.
	MacOS uint16
	// Aliases are additional accepted spellings on the wire.
	Aliases []string
	// Modifier marks keys that are held down across a shortcut.
	Modifier bool
}

// X11 keysym constants used by the table.
const (
	xkBackSpace   = 0xff08
	xkTab         = 0x0009
	xkReturn      = 0xff0d
	xkEscape      = 0xff1b
	xkDelete      = 0xffff
	xkInsert      = 0xff63
	xkHome        = 0xff50
	xkEnd         = 0xff57
	xkPrior       = 0xff55
	xkNext        = 0xff56
	xkLeft        = 0xff51
	xkUp          = 0xff52
	xkRight       = 0xff53
	xkDown        = 0xff54
	xkPrint       = 0xff61
	xkMenu        = 0xff67
	xkShiftL      = 0xffe1
	xkShiftR      = 0xffe2
	xkControlL    = 0xffe3
	xkControlR    = 0xffe4
	xkCapsLock    = 0xffe5
	xkAltL        = 0xffe9
	xkAltR        = 0xffea
	xkSuperL      = 0xffeb
	xkSuperR      = 0xffec
	xkNumLock     = 0xff7f
	xkKP0         = 0xffb0
	xkKPAdd       = 0xffab
	xkKPSubtract  = 0xffad
	xkKPMultiply  = 0xffaa
	xkKPDivide    = 0xffaf
	xkKPDecimal   = 0xffae
	xkAudioMute   = 0x1008ff12
	xkAudioLower  = 0x1008ff11
	xkAudioRaise  = 0x1008ff13
	xkAudioPlay   = 0x1008ff14
	xkAudioStop   = 0x1008ff15
	xkAudioPrev   = 0x1008ff16
	xkAudioNext   = 0x1008ff17
	xkBrowserBack = 0x1008ff26
	xkBrowserFwd  = 0x1008ff27
	xkBrowserHome = 0x1008ff29
	xkF1          = 0xffbe
)

// Carbon virtual key codes (kVK_*) used by the darwin implementation.
const (
	kvkReturn     = 0x24
	kvkTab        = 0x30
	kvkSpace      = 0x31
	kvkDelete     = 0x33
	kvkEscape     = 0x35
	kvkCommand    = 0x37
	kvkShift      = 0x38
	kvkCapsLock   = 0x39
	kvkOption     = 0x3A
	kvkControl    = 0x3B
	kvkRightCmd   = 0x36
	kvkRightShift = 0x3C
	kvkRightOpt   = 0x3D
	kvkRightCtrl  = 0x3E
	kvkF1         = 0x7A
	kvkHelp       = 0x72
	kvkHome       = 0x73
	kvkPageUp     = 0x74
	kvkForwardDel = 0x75
	kvkEnd        = 0x77
	kvkPageDown   = 0x79
	kvkLeft       = 0x7B
	kvkRight      = 0x7C
	kvkDown       = 0x7D
	kvkUp         = 0x7E
)

// keys is the complete key catalogue for protocol v1.
var keys = []KeyDef{
	// Modifiers.
	{Name: "CTRL", Windows: 0xA2, X11: xkControlL, MacOS: kvkControl, Modifier: true, Aliases: []string{"CONTROL", "LCTRL"}},
	{Name: "RCTRL", Windows: 0xA3, WindowsExtended: true, X11: xkControlR, MacOS: kvkRightCtrl, Modifier: true},
	{Name: "SHIFT", Windows: 0xA0, X11: xkShiftL, MacOS: kvkShift, Modifier: true, Aliases: []string{"LSHIFT"}},
	{Name: "RSHIFT", Windows: 0xA1, X11: xkShiftR, MacOS: kvkRightShift, Modifier: true},
	{Name: "ALT", Windows: 0xA4, WindowsExtended: true, X11: xkAltL, MacOS: kvkOption, Modifier: true, Aliases: []string{"LALT", "OPTION"}},
	{Name: "RALT", Windows: 0xA5, WindowsExtended: true, X11: xkAltR, MacOS: kvkRightOpt, Modifier: true, Aliases: []string{"ALTGR"}},
	{Name: "META", Windows: 0x5B, WindowsExtended: true, X11: xkSuperL, MacOS: kvkCommand, Modifier: true, Aliases: []string{"WIN", "SUPER", "CMD", "LWIN", "COMMAND"}},
	{Name: "RMETA", Windows: 0x5C, WindowsExtended: true, X11: xkSuperR, MacOS: kvkRightCmd, Modifier: true, Aliases: []string{"RWIN", "RSUPER"}},
	{Name: "CAPSLOCK", Windows: 0x14, X11: xkCapsLock, MacOS: kvkCapsLock, Aliases: []string{"CAPS"}},

	// Editing and navigation.
	{Name: "ENTER", Windows: 0x0D, X11: xkReturn, MacOS: kvkReturn, Aliases: []string{"RETURN"}},
	{Name: "TAB", Windows: 0x09, X11: xkTab, MacOS: kvkTab},
	{Name: "SPACE", Windows: 0x20, X11: 0x0020, MacOS: kvkSpace},
	{Name: "BACKSPACE", Windows: 0x08, X11: xkBackSpace, MacOS: kvkDelete, Aliases: []string{"BACK"}},
	{Name: "DELETE", Windows: 0x2E, WindowsExtended: true, X11: xkDelete, MacOS: kvkForwardDel, Aliases: []string{"DEL"}},
	{Name: "INSERT", Windows: 0x2D, WindowsExtended: true, X11: xkInsert, MacOS: kvkHelp, Aliases: []string{"INS"}},
	{Name: "ESCAPE", Windows: 0x1B, X11: xkEscape, MacOS: kvkEscape, Aliases: []string{"ESC"}},
	{Name: "HOME", Windows: 0x24, WindowsExtended: true, X11: xkHome, MacOS: kvkHome},
	{Name: "END", Windows: 0x23, WindowsExtended: true, X11: xkEnd, MacOS: kvkEnd},
	{Name: "PAGEUP", Windows: 0x21, WindowsExtended: true, X11: xkPrior, MacOS: kvkPageUp, Aliases: []string{"PGUP", "PRIOR"}},
	{Name: "PAGEDOWN", Windows: 0x22, WindowsExtended: true, X11: xkNext, MacOS: kvkPageDown, Aliases: []string{"PGDN", "NEXT"}},
	{Name: "UP", Windows: 0x26, WindowsExtended: true, X11: xkUp, MacOS: kvkUp, Aliases: []string{"ARROWUP"}},
	{Name: "DOWN", Windows: 0x28, WindowsExtended: true, X11: xkDown, MacOS: kvkDown, Aliases: []string{"ARROWDOWN"}},
	{Name: "LEFT", Windows: 0x25, WindowsExtended: true, X11: xkLeft, MacOS: kvkLeft, Aliases: []string{"ARROWLEFT"}},
	{Name: "RIGHT", Windows: 0x27, WindowsExtended: true, X11: xkRight, MacOS: kvkRight, Aliases: []string{"ARROWRIGHT"}},
	{Name: "PRINTSCREEN", Windows: 0x2C, WindowsExtended: true, X11: xkPrint, Aliases: []string{"PRTSC", "SNAPSHOT"}},
	{Name: "MENU", Windows: 0x5D, WindowsExtended: true, X11: xkMenu, Aliases: []string{"APPS"}},

	// Punctuation, named by its US-layout meaning. The X11 keysym is the ASCII
	// character, which is what makes these work on a US layout; a profile that
	// needs a different layout should use keyboard.text.
	{Name: "MINUS", Windows: 0xBD, X11: 0x002d, MacOS: 0x1B, Aliases: []string{"HYPHEN"}},
	{Name: "EQUAL", Windows: 0xBB, X11: 0x003d, MacOS: 0x18},
	{Name: "BRACKETLEFT", Windows: 0xDB, X11: 0x005b, MacOS: 0x21, Aliases: []string{"LBRACKET"}},
	{Name: "BRACKETRIGHT", Windows: 0xDD, X11: 0x005d, MacOS: 0x1E, Aliases: []string{"RBRACKET"}},
	{Name: "BACKSLASH", Windows: 0xDC, X11: 0x005c, MacOS: 0x2A},
	{Name: "SEMICOLON", Windows: 0xBA, X11: 0x003b, MacOS: 0x29},
	{Name: "QUOTE", Windows: 0xDE, X11: 0x0027, MacOS: 0x27, Aliases: []string{"APOSTROPHE"}},
	{Name: "BACKTICK", Windows: 0xC0, X11: 0x0060, MacOS: 0x32, Aliases: []string{"GRAVE"}},
	{Name: "COMMA", Windows: 0xBC, X11: 0x002c, MacOS: 0x2B},
	{Name: "PERIOD", Windows: 0xBE, X11: 0x002e, MacOS: 0x2F, Aliases: []string{"DOT"}},
	{Name: "SLASH", Windows: 0xBF, X11: 0x002f, MacOS: 0x2C},
	{Name: "INTLBACKSLASH", Windows: 0xE2, X11: 0x005c, MacOS: 0x0A, Aliases: []string{"ISOSECTION"}},

	// Numpad.
	{Name: "NUM0", Windows: 0x60, X11: xkKP0, MacOS: 0x52},
	{Name: "NUM1", Windows: 0x61, X11: xkKP0 + 1, MacOS: 0x53},
	{Name: "NUM2", Windows: 0x62, X11: xkKP0 + 2, MacOS: 0x54},
	{Name: "NUM3", Windows: 0x63, X11: xkKP0 + 3, MacOS: 0x55},
	{Name: "NUM4", Windows: 0x64, X11: xkKP0 + 4, MacOS: 0x56},
	{Name: "NUM5", Windows: 0x65, X11: xkKP0 + 5, MacOS: 0x57},
	{Name: "NUM6", Windows: 0x66, X11: xkKP0 + 6, MacOS: 0x58},
	{Name: "NUM7", Windows: 0x67, X11: xkKP0 + 7, MacOS: 0x59},
	{Name: "NUM8", Windows: 0x68, X11: xkKP0 + 8, MacOS: 0x5B},
	{Name: "NUM9", Windows: 0x69, X11: xkKP0 + 9, MacOS: 0x5C},
	{Name: "NUMADD", Windows: 0x6B, X11: xkKPAdd, MacOS: 0x45},
	{Name: "NUMSUBTRACT", Windows: 0x6D, X11: xkKPSubtract, MacOS: 0x4E},
	{Name: "NUMMULTIPLY", Windows: 0x6A, X11: xkKPMultiply, MacOS: 0x43},
	{Name: "NUMDIVIDE", Windows: 0x6F, WindowsExtended: true, X11: xkKPDivide, MacOS: 0x4B},
	{Name: "NUMDECIMAL", Windows: 0x6E, X11: xkKPDecimal, MacOS: 0x41},
	{Name: "NUMLOCK", Windows: 0x90, X11: xkNumLock, MacOS: 0x47},

	// Media keys: usable directly, which is why media buttons need no plugin.
	{Name: "MEDIAPLAYPAUSE", Windows: 0xB3, X11: xkAudioPlay, MacOS: 0x22},
	{Name: "MEDIANEXT", Windows: 0xB0, X11: xkAudioNext, MacOS: 0x3E + 0x60}, // kVK_FastForward
	{Name: "MEDIAPREV", Windows: 0xB1, X11: xkAudioPrev, MacOS: 0x3E + 0x5F}, // kVK_Rewind
	{Name: "MEDIASTOP", Windows: 0xB2, X11: xkAudioStop},
	{Name: "VOLUMEUP", Windows: 0xAF, X11: xkAudioRaise, MacOS: 0x48},
	{Name: "VOLUMEDOWN", Windows: 0xAE, X11: xkAudioLower, MacOS: 0x49},
	{Name: "VOLUMEMUTE", Windows: 0xAD, X11: xkAudioMute, MacOS: 0x4A},
	{Name: "BROWSERBACK", Windows: 0xA6, X11: xkBrowserBack},
	{Name: "BROWSERFORWARD", Windows: 0xA7, X11: xkBrowserFwd},
	{Name: "BROWSERHOME", Windows: 0xAC, X11: xkBrowserHome},
}

// keyIndex maps every accepted spelling to its definition.
var keyIndex = func() map[string]KeyDef {
	idx := make(map[string]KeyDef, len(keys)*3)
	for _, k := range keys {
		idx[k.Name] = k
		for _, a := range k.Aliases {
			idx[a] = k
		}
	}
	return idx
}()

// LookupKey resolves a wire key name to its definition.
//
// Letters, digits and function keys are synthesised rather than listed: 60
// mechanical entries would be noise, and the uniformity is real on all three
// platforms (X11 keysyms for ASCII letters and digits are their ASCII values).
func LookupKey(name string) (KeyDef, error) {
	n := strings.ToUpper(strings.TrimSpace(name))
	if n == "" {
		return KeyDef{}, fmt.Errorf("platform: key name must not be empty")
	}
	if d, ok := keyIndex[n]; ok {
		return d, nil
	}
	if len(n) == 1 {
		switch {
		case n[0] >= 'A' && n[0] <= 'Z':
			return KeyDef{Name: n, Windows: uint16(n[0]), X11: uint32(n[0]) + 0x20, MacOS: macLetter(n[0])}, nil
		case n[0] >= '0' && n[0] <= '9':
			return KeyDef{Name: n, Windows: uint16(n[0]), X11: uint32(n[0]), MacOS: macDigit(n[0])}, nil
		}
	}
	if strings.HasPrefix(n, "F") && len(n) <= 3 {
		var num int
		if _, err := fmt.Sscanf(n[1:], "%d", &num); err == nil && num >= 1 && num <= 24 {
			return KeyDef{
				Name: n,
				// F1..F12 are 0x70..0x7B; F13..F24 continue at 0x7C..0x87.
				Windows: uint16(0x6F + num),
				X11:     xkF1 + uint32(num) - 1,
				MacOS:   macFunction(num),
			}, nil
		}
	}
	return KeyDef{}, fmt.Errorf("platform: unknown key %q; see the key table in docs/PROTOCOL.md for the supported names", name)
}

// macLetter maps A..Z to the Carbon ANSI key codes, which follow the physical
// US layout order rather than alphabetical order.
func macLetter(c byte) uint16 {
	switch c {
	case 'A':
		return 0x00
	case 'B':
		return 0x0B
	case 'C':
		return 0x08
	case 'D':
		return 0x02
	case 'E':
		return 0x0E
	case 'F':
		return 0x03
	case 'G':
		return 0x05
	case 'H':
		return 0x04
	case 'I':
		return 0x22
	case 'J':
		return 0x26
	case 'K':
		return 0x28
	case 'L':
		return 0x25
	case 'M':
		return 0x2E
	case 'N':
		return 0x2D
	case 'O':
		return 0x1F
	case 'P':
		return 0x23
	case 'Q':
		return 0x0C
	case 'R':
		return 0x0F
	case 'S':
		return 0x01
	case 'T':
		return 0x11
	case 'U':
		return 0x20
	case 'V':
		return 0x09
	case 'W':
		return 0x0D
	case 'X':
		return 0x07
	case 'Y':
		return 0x10
	case 'Z':
		return 0x06
	}
	return 0
}

// macDigit maps '0'..'9' to the Carbon ANSI key codes.
func macDigit(c byte) uint16 {
	switch c {
	case '0':
		return 0x1D
	case '1':
		return 0x12
	case '2':
		return 0x13
	case '3':
		return 0x14
	case '4':
		return 0x15
	case '5':
		return 0x17
	case '6':
		return 0x16
	case '7':
		return 0x1A
	case '8':
		return 0x1C
	case '9':
		return 0x19
	}
	return 0
}

// macFunction maps F1..F24 to Carbon key codes. Only F1..F12 have classic
// codes; F13..F24 exist on extended keyboards and are returned as zero so the
// darwin implementation reports them as Unsupported instead of sending a wrong
// key.
func macFunction(n int) uint16 {
	f := []uint16{0x7A, 0x78, 0x63, 0x76, 0x60, 0x61, 0x62, 0x64, 0x65, 0x6D, 0x67, 0x6F}
	if n >= 1 && n <= len(f) {
		return f[n-1]
	}
	return 0
}

// IsModifier reports whether a key name is a modifier.
func IsModifier(name string) bool {
	d, err := LookupKey(name)
	return err == nil && d.Modifier
}

// KeyNames returns every canonical key name, sorted. It backs the CLI's
// `mobiledeck keys` command and the documentation table.
func KeyNames() []string {
	out := make([]string, 0, len(keys)+60)
	for _, k := range keys {
		out = append(out, k.Name)
	}
	for c := 'A'; c <= 'Z'; c++ {
		out = append(out, string(c))
	}
	for c := '0'; c <= '9'; c++ {
		out = append(out, string(c))
	}
	for i := 1; i <= 24; i++ {
		out = append(out, fmt.Sprintf("F%d", i))
	}
	sort.Strings(out)
	return out
}
