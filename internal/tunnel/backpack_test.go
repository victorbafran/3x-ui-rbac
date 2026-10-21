package tunnel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The front's end forwards UDP on the same tunnel, which is the reason to run
// BackPack at all, and mirrors each port to the panel's own listener.
func TestTheBackPackFrontForwardsUDPAndMirrorsPorts(t *testing.T) {
	cfg, err := ServerConfig(backpackPair())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[server]", `bind_addr = "0.0.0.0:3080"`, `transport = "tcp"`, `token = "a-real-token"`,
		"accept_udp = true", `"443=127.0.0.1:443"`, `"1194=127.0.0.1:1194"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("server config lacks %s:\n%s", want, cfg)
		}
	}
	// PROXY v2 breaks every service not set up to expect it, so it is not
	// switched on here; that belongs to the per-inbound real-client-IP work.
	if strings.Contains(cfg, "proxy_protocol") {
		t.Error("the front sends a PROXY header the inbounds were never told to expect")
	}
}

// The panel's end dials out and must not retune the panel's machine.
func TestThePanelsBackPackEndDoesNotRetuneThePanel(t *testing.T) {
	cfg, err := ClientConfig(backpackPair())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[client]", `remote_addr = "185.0.0.7:3080"`, "skip_optz = true", `token = "a-real-token"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("client config lacks %s:\n%s", want, cfg)
		}
	}
	p := backpackPair()
	p.FrontAddress = "2001:db8::7"
	cfg, err = ClientConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg, `remote_addr = "[2001:db8::7]:3080"`) {
		t.Errorf("an IPv6 front was written so it cannot be dialled:\n%s", cfg)
	}
}

// What is signed is the tag and the list together. A list signed for another
// release, or altered by a mirror, must be refused.
func TestOnlyAReleaseBackPackSignedIsAccepted(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sums := []byte("abc123  backpack_linux_amd64.tar.gz\n")
	sign := func(tag string, list []byte) []byte {
		msg := append([]byte("backpack release "+tag+"\n"), list...)
		return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg)) + "\n")
	}

	if err := verifyBackPackRelease("v1.8.5", sums, sign("v1.8.5", sums), pub); err != nil {
		t.Fatalf("a genuine release was refused: %v", err)
	}
	if err := verifyBackPackRelease("v1.8.5", sums, sign("v1.8.4", sums), pub); err == nil {
		t.Error("an older release's signature was accepted for a newer tag")
	}
	tampered := []byte("deadbeef  backpack_linux_amd64.tar.gz\n")
	if err := verifyBackPackRelease("v1.8.5", tampered, sign("v1.8.5", sums), pub); err == nil {
		t.Error("an altered checksum list was accepted")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := verifyBackPackRelease("v1.8.5", sums, sign("v1.8.5", sums), other); err == nil {
		t.Error("a signature by somebody else's key was accepted")
	}
	if err := verifyBackPackRelease("v1.8.5", sums, []byte("not base64!"), pub); err == nil {
		t.Error("garbage was accepted as a signature")
	}
}

// The real v1.8.5 checksum list and signature, as published, verify against
// the pinned key. If BackPack ever rotates its key this is the test that says
// so, rather than every install failing in the field. Changing one byte of the
// list must break it, or the check is not checking.
func TestThePinnedKeyVerifiesBackPacksRealRelease(t *testing.T) {
	sums, err := os.ReadFile("testdata/backpack-v1.8.5-SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile("testdata/backpack-v1.8.5-SHA256SUMS.sig")
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(backpackSigningKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		t.Fatalf("the pinned key is malformed: %v", err)
	}
	if err := verifyBackPackRelease("v1.8.5", sums, sig, ed25519.PublicKey(key)); err != nil {
		t.Fatalf("BackPack's own v1.8.5 release did not verify against the pinned key: %v", err)
	}
	altered := append([]byte(nil), sums...)
	altered[0] ^= 1
	if err := verifyBackPackRelease("v1.8.5", altered, sig, ed25519.PublicKey(key)); err == nil {
		t.Fatal("one changed byte in the checksum list still verified")
	}
	if want, err := sumFor(sums, "backpack_linux_amd64.tar.gz"); err != nil || want != "e80c4da9bdcece9e7a82305e572fdfde9a395a68a985fdd835c1af8ff248b395" {
		t.Fatalf("amd64 checksum read as %q, %v", want, err)
	}
}

func TestTheChecksumListIsReadForOneFile(t *testing.T) {
	sums := []byte("1111  backpack_linux_arm64.tar.gz\n2222 *backpack_linux_amd64.tar.gz\n")
	if got, err := sumFor(sums, "backpack_linux_amd64.tar.gz"); err != nil || got != "2222" {
		t.Fatalf("sumFor = %q, %v", got, err)
	}
	if _, err := sumFor(sums, "backpack_linux_s390x.tar.gz"); err == nil {
		t.Error("a file missing from the list was given a checksum")
	}
}

func tarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestTheBinaryIsTakenOutOfTheArchiveByName(t *testing.T) {
	archive := tarGz(t, map[string][]byte{"README.md": []byte("docs"), "backpack_linux_amd64/backpack": []byte("ELF")})
	got, err := extractBinary(archive, "backpack")
	if err != nil || string(got) != "ELF" {
		t.Fatalf("extractBinary = %q, %v", got, err)
	}
	if _, err := extractBinary(tarGz(t, map[string][]byte{"README.md": nil}), "backpack"); err == nil {
		t.Error("an archive with no binary was accepted")
	}
}

// End to end through a fake release server: a correctly signed release is
// fetched, checked and cached; a release whose archive does not match its
// signed checksum is refused and nothing is cached.
func TestFetchBackPackChecksEverythingBeforeKeepingIt(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	archive := tarGz(t, map[string][]byte{"backpack": []byte("the binary")})
	sum := sha256.Sum256(archive)
	sums := []byte(fmt.Sprintf("%s  backpack_linux_amd64.tar.gz\n", hex.EncodeToString(sum[:])))
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, append([]byte("backpack release "+BackPackRelease+"\n"), sums...)))

	serve := func(served []byte) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
				_, _ = w.Write(sums)
			case strings.HasSuffix(r.URL.Path, "/SHA256SUMS.sig"):
				_, _ = w.Write([]byte(sig))
			case strings.HasSuffix(r.URL.Path, "/backpack_linux_amd64.tar.gz"):
				_, _ = w.Write(served)
			default:
				http.NotFound(w, r)
			}
		}))
	}
	savedBase, savedKey := backpackReleaseBase, backpackSigningKeyOverride
	t.Cleanup(func() { backpackReleaseBase, backpackSigningKeyOverride = savedBase, savedKey })
	backpackSigningKeyOverride = base64.StdEncoding.EncodeToString(pub)

	good := serve(archive)
	defer good.Close()
	backpackReleaseBase = good.URL + "/"
	backpackCacheDirOverride = t.TempDir()
	t.Cleanup(func() { backpackCacheDirOverride = "" })
	bin, err := FetchBackPack(t.Context(), "amd64")
	if err != nil {
		t.Fatalf("a genuine release was refused: %v", err)
	}
	if data, _ := os.ReadFile(bin); string(data) != "the binary" {
		t.Fatalf("cached %q", data)
	}

	bad := serve(tarGz(t, map[string][]byte{"backpack": []byte("something else")}))
	defer bad.Close()
	backpackReleaseBase = bad.URL + "/"
	backpackCacheDirOverride = t.TempDir()
	if _, err := FetchBackPack(t.Context(), "amd64"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("an archive that does not match its signed checksum got through: %v", err)
	}
	if entries, _ := os.ReadDir(backpackCacheDirOverride); len(entries) != 0 {
		t.Fatalf("a refused download left %d files behind", len(entries))
	}
}

func TestTheFrontsArchitectureIsNamedTheWayReleasesAre(t *testing.T) {
	for in, want := range map[string]string{"x86_64": "amd64", "aarch64": "arm64", "armv7l": "armv7", "i686": "386"} {
		if got, err := backpackArch(in); err != nil || got != want {
			t.Errorf("backpackArch(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := backpackArch("riscv64"); err == nil {
		t.Error("an architecture BackPack does not build for was accepted")
	}
}

// The reconcile that keeps a front's ports current runs every minute. The
// first version restarted the far side unconditionally, which would have
// dropped every customer on the tunnel every minute while reporting success.
func TestTheFrontRestartsOnlyWhenSomethingChanged(t *testing.T) {
	for name, script := range map[string]string{"backhaul": remoteScript, "backpack": backpackRemoteScript} {
		if !strings.Contains(script, `cmp -s "$NEW_CONF" "$CONF_PATH"`) {
			t.Errorf("%s: the new config is never compared with the old one", name)
		}
		if !strings.Contains(script, `cmp -s "$NEW_UNIT" "$UNIT_FILE"`) {
			t.Errorf("%s: the unit is rewritten without comparing it", name)
		}
		restart := strings.Index(script, `systemctl restart "$UNIT"`)
		guard := strings.Index(script, `if [ "$CHANGED" = 1 ] || [ "$NEED" = 1 ] || ! systemctl is-active -q "$UNIT"; then`)
		if restart < 0 || guard < 0 || guard > restart {
			t.Errorf("%s: the restart is not guarded by a change or a dead unit", name)
		}
		if strings.Count(script, "systemctl restart") != 1 {
			t.Errorf("%s: something restarts the unit outside the guard", name)
		}
	}
}

// A tunnel that is torn down takes every leg with it, and BackPack's teardown
// removes only the panel's own binary.
func TestTeardownCoversEveryLegAndOnlyThePanelsBinary(t *testing.T) {
	legs := teardownLegs(Pair{Id: 4})
	if len(legs) != 2 || legs[1].unitName() != "backhaul-x-ui-4-udp" {
		t.Fatalf("backhaul teardown legs: %+v", legs)
	}
	if legs := teardownLegs(Pair{Id: 4, Tool: ToolBackPack}); len(legs) != 1 || legs[0].unitName() != "x-ui-backpack-4" {
		t.Fatalf("backpack teardown legs: %+v", legs)
	}
	if strings.Contains(backpackTeardownScript, "rm -f /usr/local/bin/backpack") {
		t.Error("tearing down a panel tunnel would delete the operator's own backpack")
	}
	if !strings.Contains(backpackTeardownScript, "x-ui-backpack-*.service") {
		t.Error("the panel's binary is removed without checking for another panel tunnel")
	}
}
