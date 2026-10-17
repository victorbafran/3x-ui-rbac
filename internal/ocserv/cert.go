package ocserv

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"
)

// selfSignedValidity is long enough that a certificate outlives the panel
// installs that generate it; the keypair is reused until an operator removes it.
const selfSignedValidity = 10 * 365 * 24 * time.Hour

// ensureKeypair returns the certificate and key files for an instance,
// generating a self-signed pair on first use and reusing it afterwards. An
// operator-supplied pair is used as-is.
//
// The generated key is RSA, never ECDSA. Cisco's own Secure Client cannot
// complete a handshake against an ECDSA server key: it fails before the
// certificate is sent, so the server looks dead rather than untrusted. ocserv
// and openconnect are both happy either way, which is exactly what makes the
// mistake survive testing.
func ensureKeypair(inst Instance) (certFile, keyFile string, err error) {
	if inst.CertFile != "" && inst.KeyFile != "" {
		return inst.CertFile, inst.KeyFile, nil
	}
	certFile, keyFile = certPathForID(inst.Id), keyPathForID(inst.Id)
	if fileExists(certFile) && fileExists(keyFile) {
		return certFile, keyFile, nil
	}
	if err := generateSelfSigned(certFile, keyFile, inst.Tag); err != nil {
		return "", "", err
	}
	return certFile, keyFile, nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}

func generateSelfSigned(certFile, keyFile, commonName string) error {
	if commonName == "" {
		commonName = "x-ui-cisco"
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generate RSA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		DNSNames:              []string{commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(selfSignedValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	// The certificate is read by ocserv's unprivileged worker, the key only by
	// the main process before it drops privileges.
	if err := writeFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return writeFile(keyFile, keyPEM, 0o600)
}

// writeFile writes atomically and then forces the mode, because the umask
// would otherwise strip bits the unprivileged ocserv worker needs.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
