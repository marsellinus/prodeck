//go:build windows

package cli

import "golang.org/x/sys/windows"

// processAlive reports whether a pid refers to a running process. It uses the
// documented "OpenProcess + zero exit code" idiom: a terminated process yields a
// non-zero exit code, so comparing against STILL_ACTIVE is the reliable test.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259
	return code == stillActive
}
