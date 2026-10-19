package relay

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Health is what the panel can say about a tunnel without asking the relay.
type Health struct {
	// Up is whether the interface exists here at all.
	Up bool
	// HandshakeAge is how long ago the two ends last agreed. WireGuard is
	// silent when idle, so an age under about three minutes means working and
	// anything older means the relay has stopped answering.
	HandshakeAge time.Duration
	// Received and Sent are the tunnel's own counters, which tell an operator
	// whether any customer has actually used this country.
	Received int64
	Sent     int64
}

// Healthy is the judgement the UI shows. PersistentKeepalive is 25 seconds, so
// a working tunnel re-handshakes well inside this window; three minutes leaves
// room for a slow link without calling a dead relay healthy.
const staleAfter = 3 * time.Minute

func (h Health) Healthy() bool { return h.Up && h.HandshakeAge > 0 && h.HandshakeAge < staleAfter }

// Check reads the local end of one tunnel.
func Check(s Server) Health {
	if runtime.GOOS != "linux" {
		return Health{}
	}
	iface := InterfaceName(s.Id)
	out, err := runLocalOut("wg", "show", iface, "dump")
	if err != nil {
		return Health{}
	}
	h := Health{Up: true}
	// wg dump prints the interface on the first line and one peer per line
	// after it: public key, preshared key, endpoint, allowed ips, last
	// handshake as a unix time, received bytes, sent bytes, keepalive.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		if ts, err := strconv.ParseInt(f[4], 10, 64); err == nil && ts > 0 {
			h.HandshakeAge = time.Since(time.Unix(ts, 0))
		}
		h.Received, _ = strconv.ParseInt(f[5], 10, 64)
		h.Sent, _ = strconv.ParseInt(f[6], 10, 64)
	}
	return h
}

// Reachability is what a customer leaving through this relay would actually
// find. An exit is only worth selling if the services people ask for answer
// it, and that is a property of the address, not of the server: the same
// request from two hosts gets two different answers.
type Reachability struct {
	Address string
	Country string
	City    string
	Org     string
	// Services maps a name people recognise to what that address gets back.
	Services map[string]string
	// Notes carries anything an operator should weigh, such as a search
	// engine that answers with a captcha.
	Notes []string
}

// reachScript asks from the relay itself, with a browser's headers. Without
// them Cloudflare answers 403 to everything and every address looks equally
// bad, which is worse than no information at all.
const reachScript = `#!/bin/bash
set -u
UA="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
H=(-H "accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
   -H "accept-language: en-US,en;q=0.9"
   -H "sec-fetch-dest: document" -H "sec-fetch-mode: navigate" -H "sec-fetch-site: none"
   -H "upgrade-insecure-requests: 1")
command -v curl >/dev/null 2>&1 || { echo "where=unknown"; echo "error=curl is not installed"; exit 0; }
info=$(curl -s --max-time 15 https://ipinfo.io/json)
echo "ip=$(echo "$info" | grep -o '"ip"[^,]*' | cut -d'"' -f4)"
echo "country=$(echo "$info" | grep -o '"country"[^,]*' | cut -d'"' -f4)"
echo "city=$(echo "$info" | grep -o '"city"[^,]*' | cut -d'"' -f4)"
echo "org=$(echo "$info" | grep -o '"org"[^,]*' | cut -d'"' -f4)"
for pair in "gemini|https://gemini.google.com/" "chatgpt|https://chatgpt.com/" "claude|https://claude.ai/" "youtube|https://www.youtube.com/" "netflix|https://www.netflix.com/"; do
  name=${pair%%|*}; url=${pair#*|}
  code=$(curl -s -o /dev/null --http2 --max-time 20 -w '%{http_code}' -A "$UA" "${H[@]}" "$url" 2>/dev/null)
  echo "service=$name:$code"
done
g=$(curl -s --max-time 20 -A "$UA" "${H[@]}" "https://www.google.com/search?q=weather" 2>/dev/null)
if echo "$g" | grep -qiE "unusual traffic|/sorry/|captcha"; then echo "service=google-search:captcha"; else echo "service=google-search:ok"; fi
`

// Reach asks the relay what the internet gives it.
func Reach(ctx context.Context, s Server) (Reachability, error) {
	key, err := os.ReadFile(s.KeyPath)
	if err != nil {
		return Reachability{}, fmt.Errorf("the key for %s is missing: %w", s.Name, err)
	}
	client, err := Dial(ctx, Credentials{Address: s.Address, Port: s.Port, User: s.User, PrivateKey: key})
	if err != nil {
		return Reachability{}, err
	}
	defer client.Close()

	out, err := client.RunScript(ctx, "xui-relay-reach.sh", reachScript)
	if err != nil {
		return Reachability{}, err
	}
	r := Reachability{Services: map[string]string{}}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ip":
			r.Address = v
		case "country":
			r.Country = v
		case "city":
			r.City = v
		case "org":
			r.Org = v
		case "service":
			name, code, _ := strings.Cut(v, ":")
			r.Services[name] = code
		}
	}
	// A 403 from Cloudflare's own bot check looks exactly like a blocked
	// country, so it is reported as unknown rather than as a failure. Only
	// a plain refusal to serve the country is worth warning about.
	if r.Services["google-search"] == "captcha" {
		r.Notes = append(r.Notes, "google-search-captcha")
	}
	if code := r.Services["gemini"]; code != "" && code != "200" {
		r.Notes = append(r.Notes, "gemini-unavailable")
	}
	return r, nil
}
