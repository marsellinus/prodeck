//go:build windows && cgo

package gui

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Closing the window must not stop the host.
//
// That is the whole point of the tray icon, and it cannot be done through the
// web view's own API: the library's window procedure destroys the window when
// the close button is pressed. So this file subclasses that procedure — the
// standard Win32 technique — and turns WM_CLOSE into a hide.
//
// The original procedure is kept and every other message is passed to it
// unchanged, so the web view behaves exactly as before apart from the close
// button.
const (
	swHide    = 0
	swShow    = 5
	swRestore = 9

	wmClose = 0x0010
	// GWLP_WNDPROC is -4, and the call takes a uintptr, so the constant is
	// written as the two's-complement value a uintptr can hold.
	gwlpWndProc = ^uintptr(3)
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")

	procShowWindow       = user32.NewProc("ShowWindow")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
	procSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProc   = user32.NewProc("CallWindowProcW")
)

// subclass holds what the replacement window procedure needs. It is package
// state because a Win32 procedure receives no user data: the callback below is
// a plain function pointer with a fixed signature.
var subclass struct {
	mu      sync.Mutex
	oldProc uintptr
	onClose func()
	proc    uintptr // keeps the callback alive for the process's lifetime
}

// hideOnClose makes the close button hide the window instead of destroying it.
//
// onClose runs on the window's own thread, inside the message loop, so it must
// return quickly. It is called instead of the default handling, which is why the
// window survives.
func hideOnClose(hwnd unsafe.Pointer, onClose func()) error {
	if hwnd == nil {
		return nil
	}
	if subclass.proc == 0 {
		subclass.proc = windows.NewCallback(func(h, msg, wParam, lParam uintptr) uintptr {
			if msg == wmClose {
				subclass.mu.Lock()
				fn := subclass.onClose
				subclass.mu.Unlock()
				if fn != nil {
					fn()
					return 0
				}
			}
			ret, _, _ := procCallWindowProc.Call(subclass.oldProc, h, msg, wParam, lParam)
			return ret
		})
	}

	subclass.mu.Lock()
	subclass.onClose = onClose
	old := subclass.oldProc
	subclass.mu.Unlock()

	if old == 0 {
		// GetWindowLongPtrW with GWLP_WNDPROC returns the current procedure.
		prev, _, err := procSetWindowLongPtr.Call(uintptr(hwnd), gwlpWndProc, subclass.proc)
		if prev == 0 && err != nil && err.Error() != "The operation completed successfully." {
			return err
		}
		subclass.mu.Lock()
		subclass.oldProc = prev
		subclass.mu.Unlock()
	}
	return nil
}

// hideWindow takes the window off the screen without destroying it, which is
// what lets the host keep running with the panel closed.
func hideWindow(hwnd unsafe.Pointer) {
	if hwnd == nil {
		return
	}
	_, _, _ = procShowWindow.Call(uintptr(hwnd), swHide)
}

// showWindow brings a hidden window back and gives it the focus.
//
// SW_RESTORE rather than SW_SHOW, so a window that was maximised before it was
// hidden comes back maximised. SetForegroundWindow is needed as well: a window
// that reappears behind the one the user is typing in looks like nothing
// happened.
func showWindow(hwnd unsafe.Pointer) {
	if hwnd == nil {
		return
	}
	_, _, _ = procShowWindow.Call(uintptr(hwnd), swRestore)
	_, _, _ = procShowWindow.Call(uintptr(hwnd), swShow)
	_, _, _ = procSetForeground.Call(uintptr(hwnd))
}
