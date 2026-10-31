package service

import (
	"errors"
	"os"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/util/certreload"
)

// ErrCertNotReferenced is a certificate path no inbound or panel setting points at.
var ErrCertNotReferenced = errors.New("certificate file is not referenced by any inbound or panel setting")

// CertCovers is what an inbound form learns about a certificate file it uses.
type CertCovers struct {
	// Host is the name the file was issued for that a client can dial; "" for none.
	Host string `json:"host" example:"vpn.example.com"`
	// Uncovered are the names asked about that a client checking the file refuses.
	Uncovered []string `json:"uncovered" example:"[\"ipv6.example.com\"]"`
}

// CertCovers reads a certificate file the panel already uses and says which of
// names it is not valid for, as AnyConnect would check them.
func (s *ServerService) CertCovers(certFile string, names []string) (CertCovers, error) {
	known, ok := s.resolveKnownCertFile(strings.TrimSpace(certFile))
	if !ok {
		return CertCovers{}, ErrCertNotReferenced
	}
	if _, err := os.Stat(known); err != nil {
		return CertCovers{}, err
	}
	res := CertCovers{Host: certreload.Host(known), Uncovered: []string{}}
	for _, name := range names {
		name = strings.Trim(strings.TrimSpace(name), "[]")
		if name != "" && !certreload.Covers(known, name) {
			res.Uncovered = append(res.Uncovered, name)
		}
	}
	return res, nil
}
