package psiphon

import "context"

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

// AnswerScanForTest makes every scan client answer answer(code) instead of running, until
// restore is called; running countries are still measured as ever. Never call it in production.
func AnswerScanForTest(answer func(code string) ScanResult) (restore func()) {
	saved := scanClient
	scanClient = func(_ *Manager, _ context.Context, _, _, code string, _ func(int) bool) ScanResult {
		r := answer(code)
		r.Code = code
		return r
	}
	return func() { scanClient = saved }
}
