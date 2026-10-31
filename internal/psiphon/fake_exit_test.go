package psiphon

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// What the stand-in's far end answers: the trace waits fakeTraceDelay, and the
// download pauses fakeDownPause halfway, so a test can bound what is measured.
const (
	fakeExitIP     = "203.0.113.7"
	fakeTraceDelay = 60 * time.Millisecond
	fakeDownPause  = 200 * time.Millisecond
	fakeDownBytes  = 1_000_000
)

// useFakeExit points the measuring at the stand-in's far end, which answers
// plain HTTP for any host a SOCKS client asks for.
func useFakeExit(t *testing.T) {
	t.Helper()
	savedTrace, savedSpeed := traceURL, speedURL
	traceURL = "http://trace.test/cdn-cgi/trace"
	speedURL = "http://speed.test/__down?bytes=" + strconv.Itoa(fakeDownBytes)
	t.Cleanup(func() { traceURL, speedURL = savedTrace, savedSpeed })
}

func listenFake(port int, serve func(net.Conn)) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if serve == nil {
				_ = c.Close()
				continue
			}
			go serve(c)
		}
	}()
	return nil
}

func logFakeStart(region string, socksPort, httpPort int, dataDir string) {
	path := os.Getenv(fakeLogEnv)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %d %d %s\n", region, socksPort, httpPort, dataDir)
}

// fakeExit takes one SOCKS5 CONNECT, whatever the target, and answers the HTTP
// request that follows as Cloudflare's trace or speed test would.
func fakeExit(c net.Conn, region string) {
	defer c.Close()
	r := bufio.NewReader(c)
	head := make([]byte, 2)
	if _, err := io.ReadFull(r, head); err != nil {
		return
	}
	if _, err := io.ReadFull(r, make([]byte, head[1])); err != nil {
		return
	}
	_, _ = c.Write([]byte{5, 0})
	req := make([]byte, 4)
	if _, err := io.ReadFull(r, req); err != nil {
		return
	}
	skip := map[byte]int{1: 4, 4: 16}[req[3]]
	if req[3] == 3 {
		n, err := r.ReadByte()
		if err != nil {
			return
		}
		skip = int(n)
	}
	if _, err := io.ReadFull(r, make([]byte, skip+2)); err != nil {
		return
	}
	_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})

	httpReq, err := http.ReadRequest(r)
	if err != nil {
		return
	}
	switch httpReq.URL.Path {
	case "/cdn-cgi/trace":
		time.Sleep(fakeTraceDelay)
		body := "fl=1\nip=" + fakeExitIP + "\nloc=" + region + "\n"
		_, _ = fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	case "/__down":
		n, _ := strconv.Atoi(httpReq.URL.Query().Get("bytes"))
		_, _ = fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", n)
		_, _ = c.Write(make([]byte, n/2))
		time.Sleep(fakeDownPause)
		_, _ = c.Write(make([]byte, n-n/2))
	default:
		_, _ = io.WriteString(c, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	}
}

// fakeStarts reads what each stand-in start logged: its country, ports and data directory.
type fakeStart struct {
	region          string
	socks, httpPort int
	dir             string
}

func fakeStarts(t *testing.T, path string) []fakeStart {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []fakeStart
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var s fakeStart
		if _, err := fmt.Sscanf(line, "%s %d %d %s", &s.region, &s.socks, &s.httpPort, &s.dir); err != nil {
			t.Fatalf("stand-in log line %q: %v", line, err)
		}
		out = append(out, s)
	}
	return out
}
