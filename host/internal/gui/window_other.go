//go:build !(windows && cgo)

package gui

import "unsafe"

// On a platform with no notification area there is nowhere to hide the window
// to, so closing it still closes it. These exist so the rest of the package
// compiles unchanged; the tray reports Available() false and the command line
// says so rather than leaving the user looking for an icon that will not appear.
func hideWindow(unsafe.Pointer) {}

func showWindow(unsafe.Pointer) {}
