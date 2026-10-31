package psiphon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// traceURL and speedURL are variables so a test can answer them; the download is
// 2 MB, a few seconds on a working tunnel and enough to tell a slow one apart.
var (
	traceURL = "https://www.cloudflare.com/cdn-cgi/trace"
	speedURL = "https://speed.cloudflare.com/__down?bytes=2000000"
)

// traceBudget and speedBudget hold one Measure inside the card's 30-second wait.
var (
	traceBudget = 10 * time.Second
	speedBudget = 15 * time.Second
)

// Exit is where a country's traffic came out and how well it moved; Err says
// which step failed, and the steps before it keep what they found.
type Exit struct {
	IP, Loc     string
	Latency     time.Duration
	BytesPerSec int64
	Err         error
}

// Works is whether the trace answered and the download moved bytes.
func (e Exit) Works() bool { return e.IP != "" && e.Err == nil }

func socksClient(socksPort int) *http.Client {
	proxy := &url.URL{Scheme: "socks5", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort))}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
}

// Trace reads Cloudflare's trace through a client's SOCKS port: the address and
// country the far end sees, and how long the whole request took.
func Trace(ctx context.Context, socksPort int) (ip, loc string, took time.Duration, err error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, traceURL, nil)
	if err != nil {
		return "", "", 0, err
	}
	resp, err := socksClient(socksPort).Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("the trace through Psiphon answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return "", "", 0, err
	}
	took = time.Since(start)
	for line := range strings.SplitSeq(string(body), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "ip":
			ip = value
		case "loc":
			loc = value
		}
	}
	if ip == "" {
		return "", "", 0, errors.New("the trace through Psiphon named no address")
	}
	return ip, loc, took, nil
}

// downloadSpeed times the body from its first header to its end; a download the
// budget cuts short is timed on what arrived, as a slow tunnel is the answer.
func downloadSpeed(ctx context.Context, socksPort int) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, speedURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := socksClient(socksPort).Do(req)
	if err != nil {
		return 0, fmt.Errorf("the download through Psiphon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the download through Psiphon answered %s", resp.Status)
	}
	start := time.Now()
	n, err := io.Copy(io.Discard, resp.Body)
	elapsed := time.Since(start)
	if n == 0 {
		if err == nil {
			err = errors.New("no bytes")
		}
		return 0, fmt.Errorf("the download through Psiphon moved nothing: %w", err)
	}
	if err != nil && ctx.Err() == nil {
		return 0, fmt.Errorf("the download through Psiphon broke off: %w", err)
	}
	return int64(float64(n) / max(elapsed.Seconds(), 1e-3)), nil
}

// Measure is the trace and then the download, each in its own budget.
func Measure(ctx context.Context, socksPort int) Exit {
	traceCtx, cancel := context.WithTimeout(ctx, traceBudget)
	ip, loc, took, err := Trace(traceCtx, socksPort)
	cancel()
	if err != nil {
		return Exit{Err: err}
	}
	speedCtx, cancel := context.WithTimeout(ctx, speedBudget)
	defer cancel()
	bps, err := downloadSpeed(speedCtx, socksPort)
	return Exit{IP: ip, Loc: loc, Latency: took, BytesPerSec: bps, Err: err}
}

// testParallel caps how many countries download at once, so they do not starve
// one another's speed on a small uplink.
const testParallel = 4

// ErrNotReached is a country the test ran out of time before it finished measuring:
// that says nothing about the country, so nothing of it is saved.
var ErrNotReached = errors.New("the test ran out of time before this country was measured")

// CountryTest is one running country measured through its own client.
type CountryTest struct {
	Code      string
	Connected bool
	Exit      Exit
}

// TestCountries measures every country whose client is running, a few at a time;
// a country with no client is left out, as there is nothing to test.
func (m *Manager) TestCountries(ctx context.Context, countries []Country) []CountryTest {
	var out []CountryTest
	var ports []int
	for _, c := range countries {
		if live := m.Status(c.Code); live.Running {
			out = append(out, CountryTest{Code: c.Code, Connected: live.Connected})
			ports = append(ports, c.SocksPort)
		}
	}
	slots := make(chan struct{}, testParallel)
	var wg sync.WaitGroup
	for i := range out {
		wg.Go(func() {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
			}
			exit := Measure(ctx, ports[i])
			// Cut off by the test's own time, before or during, a failure says nothing
			// about the country.
			if exit.Err != nil && ctx.Err() != nil {
				exit = Exit{Err: ErrNotReached}
			}
			out[i].Exit = exit
		})
	}
	wg.Wait()
	return out
}
