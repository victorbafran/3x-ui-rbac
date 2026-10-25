package openvpn

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// What the running process will say about itself: who is connected, how much
// they have moved, and — through the same socket — an order to disconnect
// somebody.

// Session is one connected device.
type Session struct {
	// Login is what the customer typed, which is how traffic is attributed
	// back to a panel client.
	Login string
	// ClientID is the process's own handle for this connection. Disconnecting
	// by id hits one device; by name it would hit every device on the
	// account, which is not what a device limit means.
	ClientID       string
	RealAddress    string
	VirtualAddress string
	BytesReceived  int64
	BytesSent      int64
	ConnectedSince time.Time
}

// parseStatus reads a status file written with status-version 2.
//
// The columns are taken from the HEADER line rather than counted, because
// OpenVPN has added columns between releases and a parser that counts starts
// attributing one customer's traffic to another the day the panel is moved to
// a newer distribution. Nothing about that failure looks like a parsing bug.
func parseStatus(raw string) []Session {
	var (
		columns  map[string]int
		sessions []Session
	)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		fields := strings.Split(strings.TrimRight(scanner.Text(), "\r"), ",")
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "HEADER":
			if fields[1] != "CLIENT_LIST" {
				continue
			}
			columns = make(map[string]int, len(fields))
			for i, name := range fields[2:] {
				columns[strings.TrimSpace(name)] = i
			}
		case "CLIENT_LIST":
			if columns == nil {
				continue
			}
			row := fields[1:]
			at := func(name string) string {
				i, ok := columns[name]
				if !ok || i >= len(row) {
					return ""
				}
				return strings.TrimSpace(row[i])
			}
			// Username is what the customer typed. Common Name is the same
			// thing here because the config sets username-as-common-name, but
			// Username is the one that stays true if that ever changes.
			login := at("Username")
			if login == "" || login == "UNDEF" {
				login = at("Common Name")
			}
			if login == "" {
				continue
			}
			s := Session{
				Login:          login,
				ClientID:       at("Client ID"),
				RealAddress:    at("Real Address"),
				VirtualAddress: at("Virtual Address"),
				BytesReceived:  atoi64(at("Bytes Received")),
				BytesSent:      atoi64(at("Bytes Sent")),
			}
			if ts := atoi64(at("Connected Since (time_t)")); ts > 0 {
				s.ConnectedSince = time.Unix(ts, 0)
			}
			sessions = append(sessions, s)
		}
	}
	return sessions
}

func atoi64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// Sessions reads the status file of one inbound.
//
// A missing file is not an error: the process writes it on a timer, so a
// freshly started inbound has none yet and that is simply nobody connected.
func Sessions(id int) []Session {
	raw, err := os.ReadFile(statusPathForID(id))
	if err != nil {
		return nil
	}
	return parseStatus(string(raw))
}

// managementTimeout keeps a wedged process from holding up a reconcile.
const managementTimeout = 5 * time.Second

// command sends one line to the management interface and returns what came
// back. The socket speaks a line protocol with no framing, so the reply is
// read until it stops rather than parsed for an end marker.
func command(socket, line string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), managementTimeout)
	defer cancel()
	dialer := net.Dialer{Timeout: managementTimeout}
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(managementTimeout))

	if _, err := fmt.Fprintf(conn, "%s\n", line); err != nil {
		return "", err
	}
	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
			if strings.Contains(out.String(), "SUCCESS:") || strings.Contains(out.String(), "ERROR:") {
				break
			}
		}
		if err != nil {
			break
		}
	}
	return out.String(), nil
}

// Disconnect drops one device by the process's own id for it.
//
// By id rather than by name: a customer over their device limit should lose
// the connection that put them over, not every connection they have.
func Disconnect(id int, clientID string) error {
	if strings.TrimSpace(clientID) == "" {
		return fmt.Errorf("no client id to disconnect")
	}
	out, err := command(managementSocketForID(id), "client-kill "+clientID)
	if err != nil {
		return err
	}
	if strings.Contains(out, "ERROR:") {
		return fmt.Errorf("the server refused to disconnect %s: %s", clientID, firstLine(out))
	}
	return nil
}

// AskToExit tells an inbound's openvpn to shut down through its management
// socket, which needs no CAP_KILL to reach a process that dropped to nobody.
func AskToExit(id int) error {
	out, err := command(managementSocketForID(id), "signal SIGTERM")
	if err != nil {
		return err
	}
	if strings.Contains(out, "ERROR:") {
		return fmt.Errorf("openvpn refused to exit: %s", firstLine(out))
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// OverLimit reports which devices are past what their account allows. The
// newest connections are kept and the oldest are dropped.
//
// It was written the other way round, and the reasoning was wrong. Dropping
// the newest looks like the stricter choice — a customer handing the account
// around cannot have each new person kick the last one off — but it never
// settles, even for one honest customer with one phone.
//
// A phone reconnects constantly: it changes network, it sleeps, it wakes.
// Each time, the server keeps the previous connection for up to a minute
// before its ping timer reaps it, so the account briefly holds a live
// session and one or more dead ones. The dead ones are older. Dropping the
// newest therefore kills the connection the customer is actually using and
// leaves the corpses holding the slots; the client reconnects, is the newest
// again, and is killed again. Measured on a real phone on 2026-10-21: two
// kills ten seconds apart, on an account with one device.
//
// Dropping the oldest converges instead. The dead sessions go first, the
// live one survives, and the cap on how many connections run at once is
// exactly the same number it always was — which is the part that makes it a
// limit. An account genuinely shared between two people still thrashes, and
// that is the behaviour being asked for.
func OverLimit(sessions []Session, limitFor func(login string) int) []Session {
	byLogin := map[string][]Session{}
	for _, s := range sessions {
		byLogin[s.Login] = append(byLogin[s.Login], s)
	}
	var over []Session
	for login, list := range byLogin {
		limit := limitFor(login)
		if limit <= 0 || len(list) <= limit {
			continue
		}
		sort.SliceStable(list, func(i, j int) bool {
			return list[i].ConnectedSince.Before(list[j].ConnectedSince)
		})
		// Oldest first, so everything before the last `limit` is dropped.
		over = append(over, list[:len(list)-limit]...)
	}
	return over
}
