package server

import (
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"strings"

	"github.com/grandcat/zeroconf"
	"github.com/mobiledeck/mobiledeck/host/internal/config"
	"github.com/mobiledeck/mobiledeck/host/internal/tlsutil"
)

// advertiser publishes the host on the LAN via mDNS/DNS-SD.
//
// Discovery is a convenience, never a dependency: the client can always be told
// an address by hand, and pairing works identically either way. That is why a
// failure here is logged and ignored rather than fatal
// (docs/adr/0008-transport-abstraction.md, docs/PROTOCOL.md §4).
type advertiser struct {
	server   *zeroconf.Server
	instance string
}

// startAdvertiser registers the service.
func startAdvertiser(cfg config.Config, tlsMat *tlsutil.Material, log *slog.Logger) (*advertiser, error) {
	instance := cfg.HostName
	if instance == "" {
		instance = "mobiledeck"
	}

	txt := []string{
		"v=1",
		"id=" + cfg.HostID,
		"name=" + sanitizeTXT(cfg.HostName),
		"os=" + runtime.GOOS,
		"port=" + fmt.Sprint(cfg.Port),
	}
	if tlsMat != nil && cfg.TLS.Enabled {
		// The fingerprint is advertised so a client can verify the host before
		// it sends a PIN, which is what defeats a rogue responder on the LAN
		// (docs/SECURITY.md T9).
		txt = append(txt, "fp="+tlsutil.NormalizeFingerprint(tlsMat.Fingerprint))
		txt = append(txt, "tls=1")
	} else {
		txt = append(txt, "tls=0")
	}

	ifaces := usableInterfaces()

	srv, err := zeroconf.Register(
		instance,
		strings.TrimPrefix(cfg.Discovery.Service, "_"), // zeroconf wants "mobiledeck" and "_tcp"
		cfg.Discovery.Domain,
		cfg.Port,
		txt,
		ifaces,
	)
	if err != nil {
		return nil, fmt.Errorf("mdns: register %q: %w", instance, err)
	}
	log.Info("mDNS advertisement published",
		"instance", instance,
		"service", cfg.Discovery.Service,
		"port", cfg.Port,
		"interfaces", len(ifaces),
		"txt", strings.Join(txt, " "),
	)
	return &advertiser{server: srv, instance: instance}, nil
}

// shutdown stops advertising. It is safe to call on a nil receiver.
func (a *advertiser) shutdown() {
	if a == nil || a.server == nil {
		return
	}
	a.server.Shutdown()
	a.server = nil
}

// usableInterfaces returns the interfaces worth advertising on. Loopback and
// down interfaces are skipped: advertising on them only produces entries that
// no phone can reach.
func usableInterfaces() []net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]net.Interface, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}
		out = append(out, iface)
	}
	return out
}

// sanitizeTXT keeps a TXT value inside the 255-byte DNS-SD limit and free of
// characters that would corrupt the record.
func sanitizeTXT(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '=' {
			return -1
		}
		return r
	}, s)
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}
