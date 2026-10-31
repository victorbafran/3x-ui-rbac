package openvpn

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"
)

// An OpenVPN server needs a certificate even when clients authenticate with a
// password, because the client verifies the server before sending it. The
// panel issues its own: a certificate authority per inbound and one server
// certificate signed by it, both generated once and reused.
//
// Reused matters. A new certificate on every restart looks to every client
// like a different server, and the client refuses it — which reads to the
// customer as the VPN having broken rather than as a certificate changing.

// certLifetime is deliberately long. A certificate that expires takes the
// inbound down on a day nobody is looking at it, and nothing is protected by
// rotating a key that never leaves the host.
const certLifetime = 10 * 365 * 24 * time.Hour

// ensureKeypair makes the authority and server certificate once. Never an operator's pair:
// each customer's .ovpn carries this authority, and a cert it does not sign stops the server.
func ensureKeypair(inst Instance) (caFile, certFile, keyFile string, err error) {
	caFile = caCertPathForID(inst.Id)
	certFile = certPathForID(inst.Id)
	keyFile = keyPathForID(inst.Id)
	caKeyFile := caFile + ".key"

	if exists(caFile) && exists(certFile) && exists(keyFile) {
		return caFile, certFile, keyFile, nil
	}

	caCert, caKey, err := makeAuthority(inst)
	if err != nil {
		return "", "", "", err
	}
	if err := writePEM(caFile, "CERTIFICATE", caCert.Raw, 0o644); err != nil {
		return "", "", "", err
	}
	if err := writePEM(caKeyFile, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(caKey), 0o600); err != nil {
		return "", "", "", err
	}

	der, key, err := makeServerCert(inst, caCert, caKey)
	if err != nil {
		return "", "", "", err
	}
	if err := writePEM(certFile, "CERTIFICATE", der, 0o644); err != nil {
		return "", "", "", err
	}
	if err := writePEM(keyFile, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), 0o600); err != nil {
		return "", "", "", err
	}
	return caFile, certFile, keyFile, nil
}

func makeAuthority(inst Instance) (*x509.Certificate, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate the authority key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: fmt.Sprintf("x-ui openvpn ca %d", inst.Id)},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(certLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("sign the authority: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func makeServerCert(inst Instance, ca *x509.Certificate, caKey *rsa.PrivateKey) ([]byte, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate the server key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: fmt.Sprintf("x-ui openvpn %d", inst.Id)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		// A client built from this panel's own profile checks that the server
		// certificate is one a server may hold. Without this the modern
		// clients refuse it outright.
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("sign the server certificate: %w", err)
	}
	return der, key, nil
}

// ensureTLSCryptKey writes the shared key that wraps every packet, generating
// it once. Both ends need the same bytes, so the profile the customer gets
// carries a copy of this file.
func ensureTLSCryptKey(id int) (string, error) {
	path := tlsAuthPathForID(id)
	if exists(path) {
		return path, nil
	}
	body, err := newStaticKey()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// newStaticKey renders 2048 bits of randomness in the shape OpenVPN's own
// --genkey writes: sixteen lines of thirty-two hex characters between a
// header and a footer it checks for.
func newStaticKey() (string, error) {
	raw := make([]byte, 256)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate the tls-crypt key: %w", err)
	}
	encoded := hex.EncodeToString(raw)

	var b strings.Builder
	b.WriteString("#\n# 2048 bit OpenVPN static key\n#\n")
	b.WriteString("-----BEGIN OpenVPN Static key V1-----\n")
	for i := 0; i < len(encoded); i += 32 {
		b.WriteString(encoded[i : i+32])
		b.WriteString("\n")
	}
	b.WriteString("-----END OpenVPN Static key V1-----\n")
	return b.String(), nil
}

func writePEM(path, kind string, der []byte, mode os.FileMode) error {
	body := pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
	if body == nil {
		return fmt.Errorf("encode %s", kind)
	}
	if err := os.WriteFile(path, body, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return os.Chmod(path, mode)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
