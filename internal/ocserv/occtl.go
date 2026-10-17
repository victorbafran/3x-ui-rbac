package ocserv

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// occtlTimeout bounds a control call. occtl talks to a local unix socket, so a
// call that has not answered in this long is a wedged server, not a slow one.
const occtlTimeout = 10 * time.Second

// Session is one live connection as occtl reports it.
//
// RX and TX are cumulative for that session and restart from zero when the
// client reconnects, which is why the manager folds them per session id rather
// than subtracting a previous total per user.
type Session struct {
	ID       int64
	Username string
	State    string
	IPv4     string
	RX       int64
	TX       int64
}

// occtlUser mirrors the fields of `occtl -j show users` the panel reads. The
// byte counters arrive as strings, so they are decoded loosely and converted.
type occtlUser struct {
	ID       json.Number `json:"ID"`
	Username string      `json:"Username"`
	State    string      `json:"State"`
	IPv4     string      `json:"IPv4"`
	RX       string      `json:"RX"`
	TX       string      `json:"TX"`
}

func parseSessions(raw []byte) ([]Session, error) {
	var users []occtlUser
	if err := json.Unmarshal(raw, &users); err != nil {
		return nil, fmt.Errorf("occtl output is not the expected JSON: %w", err)
	}
	out := make([]Session, 0, len(users))
	for _, u := range users {
		id, _ := u.ID.Int64()
		rx, _ := strconv.ParseInt(u.RX, 10, 64)
		tx, _ := strconv.ParseInt(u.TX, 10, 64)
		out = append(out, Session{
			ID:       id,
			Username: u.Username,
			State:    u.State,
			IPv4:     u.IPv4,
			RX:       rx,
			TX:       tx,
		})
	}
	return out, nil
}

func runOcctl(socket string, args ...string) ([]byte, error) {
	bin := CtlBinaryPath()
	if bin == "" {
		return nil, fmt.Errorf("occtl is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), occtlTimeout)
	defer cancel()
	full := append([]string{"-s", socket}, args...)
	out, err := exec.CommandContext(ctx, bin, full...).Output()
	if err != nil {
		return nil, fmt.Errorf("occtl %v: %w", args, err)
	}
	return out, nil
}

// listSessions asks one running instance who is connected.
func listSessions(socket string) ([]Session, error) {
	out, err := runOcctl(socket, "-j", "show", "users")
	if err != nil {
		return nil, err
	}
	return parseSessions(out)
}

// disconnectUser closes every live session of one client.
//
// Removing a client from the password file only stops the next login: ocserv
// consults the file at authentication time, so a session already established
// keeps running until it is explicitly cut. Disabling a client without this
// call leaves them connected, which is measurably not what disabling means.
func disconnectUser(socket, username string) error {
	_, err := runOcctl(socket, "disconnect", "user", username)
	return err
}
