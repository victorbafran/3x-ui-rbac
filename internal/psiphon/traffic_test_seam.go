package psiphon

// CountForTest adds moved to code's count as its client's notices would, so tests in other
// packages can drive the saving of it. Never call it in production.
func (m *Manager) CountForTest(code string, moved Traffic) { m.traffic.add(code, moved) }
