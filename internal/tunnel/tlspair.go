package tunnel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// tlsPairValidity is long because nothing renews the pair; Backhaul's client
// dials with verification off, so only the server's TLS needs a certificate.
const tlsPairValidity = 10 * 365 * 24 * time.Hour

// pairTag starts the last line of both files of one pair. The check reads it
// from each: a key and a certificate from two different runs, or a file an
// upload cut short, do not end in the same tag, so a broken pair is replaced
// on the next pass instead of being called present for ever. Go's PEM reader,
// which Backhaul loads the pair with, skips text outside the blocks.
const pairTag = "x-ui-pair "

// ensureTLSPair puts a self-signed pair on the front for a leg that needs one.
// A whole pair already there is kept: the reconcile runs this every minute.
func ensureTLSPair(ctx context.Context, r remote, leg Pair) error {
	if !leg.needsTLSPair() {
		return nil
	}
	out, err := r.Run(ctx, tlsPairCheck(leg.remoteCertPath(), leg.remoteKeyPath()))
	if err != nil {
		return fmt.Errorf("could not look for the tunnel's certificate: %w", err)
	}
	if lastLine(out) == "present" {
		return nil
	}
	cert, key, err := selfSignedPair()
	if err != nil {
		return err
	}
	// Sent on the session's input, never as an argument, so no process list sees the key.
	if err := r.Upload(ctx, leg.remoteKeyPath(), key); err != nil {
		return fmt.Errorf("could not send the tunnel's key: %w", err)
	}
	if err := r.Upload(ctx, leg.remoteCertPath(), cert); err != nil {
		return fmt.Errorf("could not send the tunnel's certificate: %w", err)
	}
	return nil
}

// tlsPairCheck prints present only when both files end in the same pair tag.
// It reads no more than the last line, so the key never crosses back either.
func tlsPairCheck(cert, key string) string {
	return fmt.Sprintf(`c=$(tail -n 1 '%s' 2>/dev/null); k=$(tail -n 1 '%s' 2>/dev/null); `+
		`case "$c" in '%s'?*) ;; *) c= ;; esac; `+
		`if [ -n "$c" ] && [ "$c" = "$k" ]; then echo present; else echo missing; fi`, cert, key, pairTag)
}

// selfSignedPair makes the certificate and key PEM for one leg. Made here
// rather than on the front, so the front needs no openssl.
func selfSignedPair() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("could not make the tunnel's key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("could not make the tunnel's certificate: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(tlsPairValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("could not make the tunnel's certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("could not encode the tunnel's key: %w", err)
	}
	tag := fmt.Sprintf("%s%032x\n", pairTag, serial)
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), tag...),
		append(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), tag...), nil
}
