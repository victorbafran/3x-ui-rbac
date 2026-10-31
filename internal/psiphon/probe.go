package psiphon

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

// The probe's ports sit just below the countries', which are handed out from
// 11181/11381 upward.
const (
	probeSocksPort = 11179
	probeHTTPPort  = 11379
)

// probeBudget, probeGrace, probeSettle and probeTick are variables so a test
// need not wait minutes for a fallback.
var (
	probeBudget = 120 * time.Second
	probeGrace  = 15 * time.Second
	probeSettle = 10 * time.Second
	probeTick   = 250 * time.Millisecond
)

// probeMu keeps two probes off the same ports and data directory.
var probeMu sync.Mutex

// DiscoverRegions asks Psiphon which countries it serves. A fresh data directory
// lists some or none (14 of 26 on 2026-10-28), so it starts twice and keeps all.
func DiscoverRegions(ctx context.Context) ([]string, error) {
	probeMu.Lock()
	defer probeMu.Unlock()

	bin, err := resolveBinary()
	if err != nil {
		return nil, err
	}
	cfg, err := ConfigJSON("", probeSocksPort, probeHTTPPort)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(Root(), "_probe")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("make %s: %w", dir, err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		return nil, fmt.Errorf("write the psiphon probe's config: %w", err)
	}

	budget, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()
	said := "it listed no country"
	var all []string
	for _, first := range []bool{true, false} {
		regions, last, err := probeOnce(budget, bin, dir, path, first)
		if err != nil {
			return nil, err
		}
		all = mergeRegions(all, regions)
		if last != "" {
			said = last
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if budget.Err() != nil {
			break
		}
	}
	if len(all) > 0 {
		return all, nil
	}
	logger.Warningf("psiphon: no live country list (%s); offering the %d known countries", said, len(FallbackRegions))
	return slices.Clone(FallbackRegions), nil
}

// mergeRegions is the sorted union of two country lists.
func mergeRegions(have, more []string) []string {
	for _, code := range more {
		if !slices.Contains(have, code) {
			have = append(have, code)
		}
	}
	slices.Sort(have)
	return have
}

// probeOnce returns every country one run announced. The first run ends once the
// server list is in; the second once its list stopped growing for probeSettle.
func probeOnce(ctx context.Context, bin, dir, path string, first bool) ([]string, string, error) {
	var mu sync.Mutex
	var found []string
	var connectedAt, grewAt time.Time
	wake := make(chan struct{}, 1)
	proc := sidecar.New(bin, "psiphon probe", "-config", path, "-dataRootDirectory", dir)
	proc.SetNoise(noticeNoise...)
	proc.OnLine(func(line string) {
		n, ok := ParseNotice(line)
		if !ok {
			return
		}
		mu.Lock()
		if regions := Regions(n); len(regions) > 0 {
			if merged := mergeRegions(slices.Clone(found), regions); len(merged) > len(found) {
				found, grewAt = merged, time.Now()
			}
		}
		if up, _ := Connected(n); up && connectedAt.IsZero() {
			connectedAt = time.Now()
		}
		mu.Unlock()
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	if err := proc.Start(); err != nil {
		return nil, "", fmt.Errorf("start the psiphon probe: %w", err)
	}
	defer func() { _ = proc.Stop() }()

	tick := time.NewTicker(probeTick)
	defer tick.Stop()
	for {
		// Read after IsRunning: a child seen gone has delivered every line.
		running := proc.IsRunning()
		mu.Lock()
		regions, upSince, grew := found, connectedAt, grewAt
		mu.Unlock()
		if !running {
			return regions, proc.Result(), nil
		}
		connectedLongEnough := !upSince.IsZero() && time.Since(upSince) >= probeGrace
		if first && (serverListFetched(dir) || connectedLongEnough) {
			return regions, "", nil
		}
		if !first && (connectedLongEnough || (len(regions) > 0 && time.Since(grew) >= probeSettle)) {
			return regions, "", nil
		}
		select {
		case <-ctx.Done():
			return regions, proc.Result(), nil
		case <-wake:
		case <-tick.C:
		}
	}
}

// serverListFetched is whether the remote server list has landed anywhere in
// the data directory; Psiphon decides the subdirectory.
func serverListFetched(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && d.Name() == "remote_server_list" {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}
