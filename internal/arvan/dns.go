package arvan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Record is one A record. raw keeps the object as Arvan sent it, because a PUT
// replaces the whole record and fields this struct does not carry must survive.
type Record struct {
	ID    string
	Type  string
	Name  string
	IPs   []string
	TTL   int
	Cloud bool
	raw   map[string]json.RawMessage
}

// recordReadOnly are the fields a PUT must not send back.
var recordReadOnly = []string{"id", "created_at", "updated_at", "usage", "is_protected"}

func (c *Client) ARecords(ctx context.Context, domain string) ([]Record, error) {
	if domain == "" {
		return nil, errors.New("arvan: no domain")
	}
	rows, err := paginate[json.RawMessage](ctx, c, "/cdn/4.0/domains/"+seg(domain)+"/dns-records", url.Values{"type": {"a"}})
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, row := range rows {
		rec, ok, err := parseARecord(row)
		if err != nil {
			return nil, fmt.Errorf("arvan: a record of %s: %w", domain, err)
		}
		if ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// parseARecord skips other types: their value is an object, not a list of IPs.
func parseARecord(row json.RawMessage) (Record, bool, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(row, &head); err != nil {
		return Record{}, false, err
	}
	if !strings.EqualFold(head.Type, "a") {
		return Record{}, false, nil
	}
	var wire struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Name  string `json:"name"`
		TTL   int    `json:"ttl"`
		Cloud bool   `json:"cloud"`
		Value []struct {
			IP string `json:"ip"`
		} `json:"value"`
	}
	if err := json.Unmarshal(row, &wire); err != nil {
		return Record{}, false, err
	}
	rec := Record{ID: wire.ID, Type: wire.Type, Name: wire.Name, TTL: wire.TTL, Cloud: wire.Cloud}
	for _, v := range wire.Value {
		rec.IPs = append(rec.IPs, v.IP)
	}
	if err := json.Unmarshal(row, &rec.raw); err != nil {
		return Record{}, false, err
	}
	return rec, true, nil
}

// SetARecordIP points the record at one IP, keeping the port, weight and country
// of its first value and every other writable field as they were.
func (c *Client) SetARecordIP(ctx context.Context, domain string, rec Record, ip string) error {
	if domain == "" || rec.ID == "" {
		return errors.New("arvan: no domain or record id")
	}
	if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("arvan: %q is not an IPv4 address", ip)
	}
	body := make(map[string]any, len(rec.raw))
	for k, v := range rec.raw {
		body[k] = v
	}
	for _, k := range recordReadOnly {
		delete(body, k)
	}
	body["type"] = "a"
	body["name"] = rec.Name
	body["cloud"] = rec.Cloud
	if rec.TTL > 0 {
		body["ttl"] = rec.TTL
	}
	value := map[string]any{"ip": ip}
	for k, v := range firstValue(rec.raw["value"]) {
		if k == "port" || k == "weight" || k == "country" {
			value[k] = v
		}
	}
	body["value"] = []any{value}
	return c.call(ctx, http.MethodPut, "/cdn/4.0/domains/"+seg(domain)+"/dns-records/"+seg(rec.ID), body, nil)
}

func firstValue(raw json.RawMessage) map[string]json.RawMessage {
	var values []map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
		return nil
	}
	return values[0]
}
