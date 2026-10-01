// Command smokeinput is a throwaway end-to-end harness for the Windows input
// layer. It is NOT part of the shipped host.
//
// It creates a real Win32 window, takes focus, and appends every character it
// receives through WM_CHAR to a file. That is the only way to prove that the
// SendInput path in internal/platform produces keystrokes a real application can
// see: asserting against a recording fake proves the call was made, not that the
// ABI was right.
//
// Usage:
//
//	smokeinput.exe <output-file> <ready-file>
//
// It writes the ready file once the window is focused, then runs until killed.
package main

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procShowWindow          = user32.NewProc("ShowWindow")
	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	swShow             = 5
	wmDestroy          = 0x0002
	wmChar             = 0x0102
	cwUseDefault       = ^0x7fffffff
)

type wndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type msg struct {
	Hwnd    windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

var (
	outPath   string
	readyPath string
	outFile   *os.File
	logFile   *os.File
)

func main() {
	runtime.LockOSThread() // the window must stay on one thread
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: smokeinput <output-file> <ready-file>")
		os.Exit(2)
	}
	outPath, readyPath = os.Args[1], os.Args[2]

	var err error
	outFile, err = os.Create(outPath)
	if err != nil {
		fail("creating the output file: %v", err)
	}
	defer outFile.Close()

	logFile, err = os.Create(outPath + ".log")
	if err != nil {
		fail("creating the message log: %v", err)
	}
	defer logFile.Close()

	className := utf16ptr("MobileDeckSmokeInput")
	instance, _, _ := procGetModuleHandleW.Call(0)

	wc := wndClassExW{
		Size:      uint32(unsafe.Sizeof(wndClassExW{})),
		WndProc:   syscall.NewCallback(wndProc),
		Instance:  windows.Handle(instance),
		ClassName: className,
	}
	if ret, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
		fail("RegisterClassExW: %v", err)
	}

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16ptr("MobileDeck smoke input"))),
		wsOverlappedWindow,
		100, 100, 420, 200,
		0, 0, instance, 0,
	)
	if hwnd == 0 {
		fail("CreateWindowExW: %v", err)
	}

	procShowWindow.Call(hwnd, swShow)

	// Take focus. A newly created window's process can usually set itself
	// foreground, but the attempt can be refused while the foreground lock is
	// held, so it is retried. The ready file is written only once focus has
	// actually been acquired, because a driver that starts typing too early
	// would type into whatever had focus before and the test would report a
	// false failure.
	focused := false
	for range 20 {
		time.Sleep(100 * time.Millisecond)
		if ret, _, _ := procSetForegroundWindow.Call(hwnd); ret != 0 {
			// Confirm with the OS that this window really is foreground.
			if cur, _, _ := procGetForegroundWindow.Call(); cur == uintptr(hwnd) {
				focused = true
				break
			}
		}
	}
	if !focused {
		fail("could not take the foreground after 2 seconds; close other windows that may be holding focus")
	}
	time.Sleep(200 * time.Millisecond)

	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		fail("writing the ready file: %v", err)
	}

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(hwnd windows.Handle, message uint32, wparam, lparam uintptr) uintptr {
	// Every message is logged so a failure can be diagnosed: an absent
	// WM_KEYDOWN means the input never arrived, while a WM_KEYDOWN without a
	// WM_CHAR means TranslateMessage rejected the event.
	if logFile != nil {
		fmt.Fprintf(logFile, "msg=0x%04X wparam=0x%04X lparam=0x%08X\n", message, wparam, lparam)
		logFile.Sync()
	}
	switch message {
	case wmChar:
		// Record the character as the receiving application would see it.
		outFile.WriteString(string(rune(wparam)))
		outFile.Sync()
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wparam, lparam)
	return ret
}

func utf16ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		fail("converting %q: %v", s, err)
	}
	return p
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "smokeinput: "+format+"\n", args...)
	os.Exit(1)
}

var _ = cwUseDefault
