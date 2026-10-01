// Command mobiledeck is the MobileDeck host agent: it turns a phone into a
// stream deck for this machine.
//
// The binary is self-contained. It embeds the HTTP server, the WebSocket
// endpoint, the mDNS responder and the action engine, and it runs directly from
// the filesystem with no container, no service manager and no cloud
// (docs/adr/0009-single-binary-no-docker-dependency.md).
package main

import (
	"os"

	"github.com/mobiledeck/mobiledeck/host/internal/cli"
)

// version is overridden at build time with
// -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	os.Exit(cli.Run(cli.Env{
		Args:    os.Args[1:],
		Version: version,
	}))
}
