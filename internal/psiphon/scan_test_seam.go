package psiphon

// UseBinaryForTest makes every client, a scan's included, run bin until restore is
// called, so tests in other packages need no download. Never call it in production.
func UseBinaryForTest(bin string) (restore func()) {
	saved := resolveBinary
	resolveBinary = func() (string, error) { return bin, nil }
	return func() { resolveBinary = saved }
}

// HoldScanPortForTest marks port as a scan client's until release is called, as a
// running scan holds it. Never call it in production.
func HoldScanPortForTest(port int) (release func()) {
	m := GetManager()
	m.scan.mu.Lock()
	if m.scan.held == nil {
		m.scan.held = map[int]bool{}
	}
	m.scan.held[port] = true
	m.scan.mu.Unlock()
	return func() { m.scan.giveBack(port) }
}
