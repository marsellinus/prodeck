//go:build windows

package platform

import "os"

// e2eConsoleEnabled gates the tests that open a real console window, so a normal
// `go test ./...` never steals focus or leaves a shell behind.
func e2eConsoleEnabled() bool { return os.Getenv("MOBILEDECK_E2E_CONSOLE") != "" }
