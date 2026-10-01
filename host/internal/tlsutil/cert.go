// Package tlsutil generates and inspects the host's self-signed certificate.
//
// A self-signed certificate with a published fingerprint is what makes the LAN
// channel both confidential and verifiable without a CA, a public domain name,
// or any internet access (docs/adr/0006-tls-fingerprint-pinning.md).
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CertFile and KeyFile are the conventional names inside the TLS directory.
const (
	CertFile = "cert.pem"
	KeyFile  = "key.pem"
)

// Material is a loaded certificate and its fingerprint.
type Material struct {
	Cert        tls.Certificate
	Fingerprint string // colon-separated uppercase SHA-256, as the client displays it
	NotAfter    time.Time
	DNSNames    []string
	IPs         []string
}

// LoadOrGenerate returns the certificate in dir, creating a self-signed one on
// first run. validDays is used only when generating.
func LoadOrGenerate(dir, hostName string, validDays int) (*Material, error) {
	certPath := filepath.Join(dir, CertFile)
	keyPath := filepath.Join(dir, KeyFile)

	if _, err := os.Stat(certPath); err == nil {
		if _, err := os.Stat(keyPath); err != nil {
			return nil, fmt.Errorf("tlsutil: %s exists but %s does not; remove the certificate directory to regenerate both", certPath, keyPath)
		}
		return Load(certPath, keyPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("tlsutil: stat %s: %w", certPath, err)
	}

	m, err := Generate(hostName, validDays)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("tlsutil: create %s: %w", dir, err)
	}
	if err := os.WriteFile(certPath, m.CertPEM, 0o600); err != nil {
		return nil, fmt.Errorf("tlsutil: write %s: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, m.KeyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("tlsutil: write %s: %w", keyPath, err)
	}
	return m.Material, nil
}

// generated bundles the encoded forms with the parsed material.
type generated struct {
	*Material
	CertPEM []byte
	KeyPEM  []byte
}

// Generate creates a self-signed ECDSA P-256 certificate for this machine.
//
// Every local unicast address is included as a SAN, because the client connects
// by IP: a certificate that only names the hostname would fail verification
// before the fingerprint pin is even consulted.
func Generate(hostName string, validDays int) (*generated, error) {
	if validDays <= 0 {
		validDays = 825
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: generate key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: generate serial: %w", err)
	}

	ips, err := LocalIPs()
	if err != nil {
		// A machine with no usable interface can still serve loopback.
		ips = []net.IP{net.IPv4(127, 0, 0, 1)}
	}

	dnsNames := []string{"localhost", "mobiledeck.local"}
	if hostName != "" {
		dnsNames = append(dnsNames, hostName)
		if !strings.Contains(hostName, ".") {
			dnsNames = append(dnsNames, hostName+".local")
		}
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   firstNonEmpty(hostName, "mobiledeck"),
			Organization: []string{"MobileDeck"},
			// The OU carries the marker the client looks for before it will
			// accept a certificate it cannot chain to a CA.
			OrganizationalUnit: []string{"MobileDeck Self-Signed Host"},
		},
		NotBefore:             now.Add(-1 * time.Hour), // tolerate a small clock difference
		NotAfter:              now.AddDate(0, 0, validDays),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true, // self-signed leaf that also acts as its own trust anchor
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: marshal key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: assemble key pair: %w", err)
	}

	ipsOut := make([]string, 0, len(ips))
	for _, ip := range ips {
		ipsOut = append(ipsOut, ip.String())
	}
	return &generated{
		Material: &Material{
			Cert:        pair,
			Fingerprint: FingerprintOf(der),
			NotAfter:    template.NotAfter,
			DNSNames:    dnsNames,
			IPs:         ipsOut,
		},
		CertPEM: certPEM,
		KeyPEM:  keyPEM,
	}, nil
}

// Load reads an existing certificate and key pair.
func Load(certPath, keyPath string) (*Material, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: read %s: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: read %s: %w", keyPath, err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: load key pair: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return nil, fmt.Errorf("tlsutil: %s contains no certificate", certPath)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("tlsutil: parse certificate: %w", err)
	}
	pair.Leaf = leaf

	ips := make([]string, 0, len(leaf.IPAddresses))
	for _, ip := range leaf.IPAddresses {
		ips = append(ips, ip.String())
	}
	return &Material{
		Cert:        pair,
		Fingerprint: FingerprintOf(pair.Certificate[0]),
		NotAfter:    leaf.NotAfter,
		DNSNames:    leaf.DNSNames,
		IPs:         ips,
	}, nil
}

// FingerprintOf renders the SHA-256 of a DER certificate in the colon-separated
// uppercase hex form the client shows and compares.
func FingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, 0, len(sum))
	for _, b := range sum {
		parts = append(parts, strings.ToUpper(hex.EncodeToString([]byte{b})))
	}
	return strings.Join(parts, ":")
}

// NormalizeFingerprint accepts a fingerprint with or without colons and in any
// case, so a user pasting one from a terminal is not defeated by formatting.
func NormalizeFingerprint(s string) string {
	clean := strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(s))
	return strings.ToUpper(clean)
}

// Matches reports whether a presented fingerprint matches the expected one.
// Comparison is on the normalised form and is length-checked first.
func Matches(expected, presented string) bool {
	a := NormalizeFingerprint(expected)
	b := NormalizeFingerprint(presented)
	if a == "" || b == "" || len(a) != len(b) {
		return false
	}
	// A fingerprint is public information, so a non-constant-time comparison
	// leaks nothing; the length check above is for correctness, not secrecy.
	return a == b
}

// LocalIPs returns every usable unicast address on this machine.
func LocalIPs() ([]net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("tlsutil: list interfaces: %w", err)
	}
	var out []net.IP
	seen := map[string]bool{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP
			if ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
				continue
			}
			if seen[ip.String()] {
				continue
			}
			seen[ip.String()] = true
			out = append(out, ip)
		}
	}
	// Loopback is always valid: it is how a local test client connects.
	if !seen["127.0.0.1"] {
		out = append(out, net.IPv4(127, 0, 0, 1))
	}
	return out, nil
}

// PreferredIP picks the address a phone on the same LAN is most likely able to
// reach: the first private IPv4 address, falling back to any IPv4, then IPv6.
// It is used for the mDNS TXT record and for the pairing QR payload.
func PreferredIP() (net.IP, error) {
	ips, err := LocalIPs()
	if err != nil {
		return nil, err
	}
	var fallback net.IP
	for _, ip := range ips {
		v4 := ip.To4()
		if v4 == nil {
			if fallback == nil {
				fallback = ip
			}
			continue
		}
		if v4.IsLoopback() {
			continue
		}
		if v4.IsPrivate() {
			return v4, nil
		}
		if fallback == nil {
			fallback = v4
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	return net.IPv4(127, 0, 0, 1), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
