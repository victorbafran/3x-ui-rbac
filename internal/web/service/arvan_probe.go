package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// check-host.net is asked from its own Iranian nodes; a plain client, never the
// panel's egress proxy, since the question is what Iran sees of the front.
var (
	checkHostBase   = "https://check-host.net"
	checkHostClient = &http.Client{Timeout: 10 * time.Second}
)

const (
	probeDialTimeout  = 4 * time.Second
	checkHostMaxNodes = 4
	checkHostWait     = 15 * time.Second
	checkHostNodesTTL = time.Hour
)

var checkHostNodes struct {
	sync.Mutex
	names []string
	at    time.Time
}

// probeIP is whether an IP can carry the tunnel: the panel reaches its tunnel
// port, and Iran does too where check-host answered at all.
func probeIP(ctx context.Context, ip string, port int) (ok bool, iranOk, iranTotal int, err error) {
	if err := dialTries(ctx, ip, port, probeDialTimeout, 2); err != nil {
		return false, 0, 0, err
	}
	iranOk, iranTotal = checkHostIran(ctx, ip, port)
	if iranTotal > 0 && iranOk == 0 {
		return false, 0, iranTotal, fmt.Errorf("none of %d Iranian vantage points reached %s:%d", iranTotal, ip, port)
	}
	return true, iranOk, iranTotal, nil
}

// dialTries opens and closes one TCP connection, retrying a dropped SYN.
func dialTries(ctx context.Context, ip string, port int, timeout time.Duration, tries int) error {
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	var err error
	for range tries {
		d := net.Dialer{Timeout: timeout}
		var conn net.Conn
		if conn, err = d.DialContext(ctx, "tcp", addr); err == nil {
			_ = conn.Close()
			return nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return fmt.Errorf("the panel cannot reach %s: %w", addr, err)
}

// panelControls are well-known hosts the panel should always reach; when none
// answers, the panel is offline and no front IP can be judged from here.
var panelControls = []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}

// panelOnline reports whether the panel itself still reaches the internet.
func panelOnline(ctx context.Context) error {
	var err error
	for _, ip := range panelControls {
		if err = dialTries(ctx, ip, 443, 3*time.Second, 1); err == nil {
			return nil
		}
	}
	return err
}

// checkHostIran counts the Iranian nodes that reached ip:port. A node still
// silent at the deadline is left out of the total: silence is not a verdict.
func checkHostIran(ctx context.Context, ip string, port int) (reached, answered int) {
	nodes := iranianNodes(ctx)
	if len(nodes) == 0 {
		return 0, 0
	}
	q := url.Values{}
	q.Set("host", net.JoinHostPort(ip, strconv.Itoa(port)))
	for _, n := range nodes {
		q.Add("node", n)
	}
	var started struct {
		RequestID string `json:"request_id"`
	}
	if err := checkHostGet(ctx, "/check-tcp?"+q.Encode(), &started); err != nil || started.RequestID == "" {
		return 0, 0
	}

	deadline := time.Now().Add(checkHostWait)
	for {
		select {
		case <-ctx.Done():
			return reached, answered
		case <-time.After(2 * time.Second):
		}
		var results map[string]json.RawMessage
		if err := checkHostGet(ctx, "/check-result/"+url.PathEscape(started.RequestID), &results); err == nil {
			reached, answered = tallyCheckHost(results)
			if answered == len(nodes) {
				return reached, answered
			}
		}
		if time.Now().After(deadline) {
			return reached, answered
		}
	}
}

// tallyCheckHost reads a check-tcp result: null while a node is pending, else
// a list holding {"time","address"} on success or {"error"} on failure.
func tallyCheckHost(results map[string]json.RawMessage) (reached, answered int) {
	for _, raw := range results {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "" || trimmed == "null" {
			continue
		}
		var entries []map[string]any
		if json.Unmarshal(raw, &entries) != nil {
			var one map[string]any
			if json.Unmarshal(raw, &one) != nil {
				continue
			}
			entries = []map[string]any{one}
		}
		pending := true
		ok := false
		for _, e := range entries {
			if e == nil {
				continue
			}
			pending = false
			_, failed := e["error"]
			_, hasTime := e["time"]
			_, hasAddr := e["address"]
			if !failed && (hasTime || hasAddr) {
				ok = true
			}
		}
		if pending {
			continue
		}
		answered++
		if ok {
			reached++
		}
	}
	return reached, answered
}

// iranianNodes is check-host's nodes located in Iran, cached for an hour.
func iranianNodes(ctx context.Context) []string {
	checkHostNodes.Lock()
	defer checkHostNodes.Unlock()
	if len(checkHostNodes.names) > 0 && time.Since(checkHostNodes.at) < checkHostNodesTTL {
		return checkHostNodes.names
	}
	var listing struct {
		Nodes map[string]struct {
			Location []string `json:"location"`
		} `json:"nodes"`
	}
	if err := checkHostGet(ctx, "/nodes/hosts", &listing); err != nil {
		return checkHostNodes.names
	}
	var names []string
	for name, n := range listing.Nodes {
		if len(n.Location) > 0 && strings.EqualFold(n.Location[0], "ir") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > checkHostMaxNodes {
		names = names[:checkHostMaxNodes]
	}
	checkHostNodes.names, checkHostNodes.at = names, time.Now()
	return names
}

func checkHostGet(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkHostBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := checkHostClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("check-host: %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
