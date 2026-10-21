// Package certissue obtains publicly trusted certificates for the VPN
// sidecars from Let's Encrypt, and keeps them renewed.
//
// It exists because the certificate was the hardest part of the first real
// cisco inbound. A self-signed one is refused by the official client, a real
// one has to be fetched over port 80 of whatever the name points at, and a
// real one renewed at the same path was never re-read by ocserv. Every one of
// those was done by hand once; the panel now does all three.
package certissue

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

// RenewBefore is how long before expiry a certificate is renewed. Let's
// Encrypt certificates live 90 days; checking twice a day with a month of
// margin leaves sixty chances to succeed before a customer ever meets an
// expired certificate.
const RenewBefore = 30 * 24 * time.Hour

// maxNames is Let's Encrypt's own limit on names in one certificate.
const maxNames = 100

var (
	// directoryURL is Let's Encrypt's production directory.
	directoryURL = acme.LetsEncryptURL
	// challengeAddr is where the HTTP-01 answer is served. Port 80 is not a
	// choice: it is the only port Let's Encrypt fetches the proof from.
	challengeAddr = ":80"
	// baseDirOverride lets a test write somewhere harmless.
	baseDirOverride string
)

// issueMu keeps issuance to one at a time. Two would fight over port 80, and
// the loser would fail with a message about the port rather than the truth.
var issueMu sync.Mutex

// Result is where a certificate landed and how long it is good for.
type Result struct {
	CertFile string    `json:"certFile"`
	KeyFile  string    `json:"keyFile"`
	Names    []string  `json:"names"`
	NotAfter time.Time `json:"notAfter"`
	// Reused is true when a certificate for exactly these names, with more
	// than RenewBefore left, was handed back instead of a new one issued.
	Reused bool `json:"reused"`
}

func baseDir() string {
	if baseDirOverride != "" {
		return baseDirOverride
	}
	return filepath.Join(config.GetDBFolderPath(), "certs")
}

// certDir is named after the first name, which is also the certificate's
// common name, so an operator looking on disk finds it where they expect.
func certDir(primary string) string {
	return filepath.Join(baseDir(), primary)
}

// NormalizeNames lowercases, trims and deduplicates the names, keeping their
// order, and refuses what HTTP-01 cannot prove: an address, a wildcard, or
// something that is not a hostname at all.
func NormalizeNames(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, r := range raw {
		name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r)), ".")
		if name == "" || seen[name] {
			continue
		}
		if net.ParseIP(name) != nil {
			return nil, fmt.Errorf("%s is an address, not a name; the certificate is for the names customers type", name)
		}
		if strings.Contains(name, "*") {
			return nil, fmt.Errorf("%s is a wildcard, which cannot be proved over port 80; list each name instead", name)
		}
		if !validHostname(name) {
			return nil, fmt.Errorf("%q is not a hostname", name)
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, errors.New("no name given; enter the name customers connect to")
	}
	if len(out) > maxNames {
		return nil, fmt.Errorf("%d names is more than the %d one certificate can carry", len(out), maxNames)
	}
	return out, nil
}

// validHostname accepts letters, digits and hyphens in dot-separated labels
// of at most 63 characters, at least two labels, 253 characters in all.
func validHostname(name string) bool {
	if len(name) > 253 {
		return false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// Issue returns a certificate for the names, fetching one from Let's Encrypt
// unless the panel already holds one for exactly these names with plenty of
// life left. Let's Encrypt allows five duplicates of a certificate a week, and
// an operator pressing the button twice should not spend one.
func Issue(ctx context.Context, rawNames []string) (Result, error) {
	names, err := NormalizeNames(rawNames)
	if err != nil {
		return Result{}, err
	}
	issueMu.Lock()
	defer issueMu.Unlock()

	dir := certDir(names[0])
	if meta, err := readMeta(dir); err == nil && sameNames(meta.Names, names) &&
		time.Until(meta.NotAfter) > RenewBefore && pairExists(dir) {
		return resultFor(dir, meta, true), nil
	}
	return issueLocked(ctx, dir, names)
}

// issueLocked runs one ACME order. The caller holds issueMu.
func issueLocked(ctx context.Context, dir string, names []string) (Result, error) {
	accountKey, err := loadOrCreateAccountKey()
	if err != nil {
		return Result{}, err
	}
	client := &acme.Client{Key: accountKey, DirectoryURL: directoryURL, UserAgent: "3x-ui-rbac"}
	if _, err := client.Register(ctx, &acme.Account{}, acme.AcceptTOS); err != nil &&
		!errors.Is(err, acme.ErrAccountAlreadyExists) {
		return Result{}, fmt.Errorf("could not register with Let's Encrypt: %w", err)
	}

	// The answer has to be listening before Let's Encrypt is told to look.
	answers := newChallengeServer()
	stop, err := answers.listen(ctx, challengeAddr)
	if err != nil {
		return Result{}, err
	}
	defer stop()

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(names...))
	if err != nil {
		return Result{}, fmt.Errorf("Let's Encrypt refused to start an order for %s: %w", strings.Join(names, ", "), err)
	}
	for _, url := range order.AuthzURLs {
		if err := prove(ctx, client, answers, url); err != nil {
			return Result{}, err
		}
	}
	if _, err := client.WaitOrder(ctx, order.URI); err != nil {
		return Result{}, fmt.Errorf("the order for %s did not become ready: %w", strings.Join(names, ", "), err)
	}

	// RSA, never ECDSA: some versions of Cisco Secure Client offer no ECDSA
	// cipher at all and give up before the certificate is even sent, and an
	// RSA key costs the clients that do support ECDSA nothing.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return Result{}, fmt.Errorf("no randomness for a key: %w", err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: names[0]},
		DNSNames: names,
	}, key)
	if err != nil {
		return Result{}, fmt.Errorf("could not build the certificate request: %w", err)
	}
	chain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return Result{}, fmt.Errorf("Let's Encrypt did not issue the certificate: %w", err)
	}
	if len(chain) == 0 {
		return Result{}, errors.New("Let's Encrypt returned an empty certificate chain")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return Result{}, fmt.Errorf("the issued certificate does not parse: %w", err)
	}
	meta := Meta{Names: names, NotAfter: leaf.NotAfter.UTC(), Issued: time.Now().UTC()}
	if err := writeFiles(dir, chain, key, meta); err != nil {
		return Result{}, err
	}
	return resultFor(dir, meta, false), nil
}

// prove answers one name's HTTP-01 challenge and waits for Let's Encrypt to
// accept it. A name already proved on this account within the validity
// window comes back valid and needs nothing.
func prove(ctx context.Context, client *acme.Client, answers *challengeServer, authzURL string) error {
	authz, err := client.GetAuthorization(ctx, authzURL)
	if err != nil {
		return fmt.Errorf("could not read the challenge from Let's Encrypt: %w", err)
	}
	if authz.Status == acme.StatusValid {
		return nil
	}
	name := authz.Identifier.Value
	var chal *acme.Challenge
	for _, c := range authz.Challenges {
		if c.Type == "http-01" {
			chal = c
			break
		}
	}
	if chal == nil {
		return fmt.Errorf("Let's Encrypt offered %s no challenge over port 80", name)
	}
	body, err := client.HTTP01ChallengeResponse(chal.Token)
	if err != nil {
		return fmt.Errorf("could not compute the answer for %s: %w", name, err)
	}
	answers.set(client.HTTP01ChallengePath(chal.Token), body)
	if _, err := client.Accept(ctx, chal); err != nil {
		return fmt.Errorf("Let's Encrypt would not check %s: %w", name, err)
	}
	if _, err := client.WaitAuthorization(ctx, authz.URI); err != nil {
		return explainProof(name, err)
	}
	return nil
}

// explainProof turns a failed check into what the operator has to change.
// Almost every failure here is the same one: the name does not lead port 80
// back to this panel.
func explainProof(name string, err error) error {
	return fmt.Errorf("Let's Encrypt could not fetch the proof for %s from port 80 of the address it points at. "+
		"The name has to point at this panel, or at an entry server whose tunnel carries port 80 to it, "+
		"and nothing in between may block port 80: %w", name, err)
}

// RenewDue renews every certificate the panel issued that has less than
// RenewBefore left. ocserv and OpenVPN pick the new files up on their own:
// their restart fingerprints cover the certificate's content, not only its
// path.
func RenewDue(ctx context.Context, now time.Time) []error {
	entries, err := os.ReadDir(baseDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return []error{err}
	}
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(baseDir(), e.Name())
		meta, err := readMeta(dir)
		if err != nil {
			// A directory the panel did not write, or one it never finished.
			continue
		}
		if !dueForRenewal(meta.NotAfter, now) {
			continue
		}
		issueMu.Lock()
		_, err = issueLocked(ctx, dir, meta.Names)
		issueMu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("renewing %s: %w", strings.Join(meta.Names, ", "), err))
		}
	}
	return errs
}

func dueForRenewal(notAfter, now time.Time) bool {
	return !now.Add(RenewBefore).Before(notAfter)
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func resultFor(dir string, meta Meta, reused bool) Result {
	return Result{
		CertFile: filepath.Join(dir, certFileName),
		KeyFile:  filepath.Join(dir, keyFileName),
		Names:    meta.Names,
		NotAfter: meta.NotAfter,
		Reused:   reused,
	}
}
