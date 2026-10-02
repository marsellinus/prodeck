// Package tray puts the control panel's host in the notification area, so the
// window can be closed without the host stopping.
//
// It exists because "close the window" and "stop the host" are not the same
// wish. Before this, closing the panel stopped the host, which meant a phone
// lost its deck the moment the user tidied their desktop. With a tray icon the
// window is just a window: closing it leaves the host running, and the icon is
// how the window is opened again.
//
// The tray is deliberately not a second control surface. It shows the window, it
// says where the deck is reachable, and it quits. Everything else is in the
// panel, which talks to the host over the same admin API the command line uses
// (docs/adr/0011-desktop-gui.md).
package tray

// Options configures the icon.
type Options struct {
	// Title is the tooltip, shown when the pointer rests on the icon. It names
	// the machine, because more than one deck on a desk is the common case.
	Title string
	// Address is where the deck is reachable, shown as a menu line that cannot
	// be clicked. It answers "what do I type into the phone" without a window.
	Address string
}

// Callbacks are what the tray does. They are passed in rather than reached for
// so this package stays free of any dependency on the host or the window.
type Callbacks struct {
	// Show is called when the user asks for the window: a left click, a double
	// click, or the menu item. It must be safe to call from another goroutine,
	// which is where the tray's event loop lives.
	Show func()
	// Quit is called when the user asks to stop. The tray icon is removed only
	// when Stop is called, so the host has time to shut down cleanly first.
	Quit func()
}
