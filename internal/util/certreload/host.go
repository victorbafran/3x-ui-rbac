package certreload

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"
)

// Host is the name a certificate file was issued for that a client can dial:
// the first domain with a dot and no wildcard, else the first public IP.
func Host(certFile string) string {
	raw, err := os.ReadFile(certFile)
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return ""
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	for _, name := range leaf.DNSNames {
		if !strings.Contains(name, "*") && strings.Contains(name, ".") {
			return name
		}
	}
	for _, ip := range leaf.IPAddresses {
		if !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsPrivate() {
			return ip.String()
		}
	}
	return ""
}
