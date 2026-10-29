package sidecar

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// foldWindow is how often folded noise is reported as one count line.
const foldWindow = 10 * time.Minute

// ansiColour matches the SGR codes some helpers print even when not on a
// terminal; in the panel log they are raw bytes around the text.
var ansiColour = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// NoiseRule is a line a helper prints for every stranger who knocks, folded
// into a periodic count so it cannot push real lines out of the panel log.
type NoiseRule struct {
	Kind string
	Line *regexp.Regexp
	// Continuation matches the lines that belong to a folded one, and is only
	// tried on the lines straight after it.
	Continuation *regexp.Regexp
	// Silent keeps the line out of the count too: one the caller reads many times
	// a second, whose count would be a log line of its own every window.
	Silent bool
}

// LineWriter forwards a child's output into the panel log line by line, and
// keeps the last real line: what the panel shows for an instance that will not start.
type LineWriter struct {
	mu       sync.Mutex
	label    string
	noise    []NoiseRule
	now      func() time.Time
	buf      string
	lastLine string
	// folding is the rule whose continuation lines may come next.
	folding *NoiseRule
	folded  map[string]int
	since   time.Time
	onLine  func(line string)
}

// NewLineWriter prefixes every line with label, and folds lines matching noise.
func NewLineWriter(label string, noise ...NoiseRule) *LineWriter {
	return &LineWriter{label: label, noise: noise, now: time.Now, folded: map[string]int{}}
}

func (w *LineWriter) setNoise(noise []NoiseRule) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.noise = noise
}

func (w *LineWriter) setOnLine(fn func(line string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onLine = fn
}

func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf += string(p)
	var seen []string
	for {
		i := strings.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i]
		w.buf = w.buf[i+1:]
		if line = w.emitLocked(line); line != "" && w.onLine != nil {
			seen = append(seen, line)
		}
	}
	onLine := w.onLine
	w.mu.Unlock()
	tell(onLine, seen)
	return len(p), nil
}

// Flush emits a final line that never got its newline, which is how a process
// that dies mid-sentence still says what went wrong, then the pending count.
func (w *LineWriter) Flush() {
	w.mu.Lock()
	var seen []string
	if w.buf != "" {
		line := w.buf
		w.buf = ""
		if line = w.emitLocked(line); line != "" {
			seen = append(seen, line)
		}
	}
	w.reportFoldedLocked(w.now(), true)
	onLine := w.onLine
	w.mu.Unlock()
	tell(onLine, seen)
}

// tell runs outside the writer's lock, so a callback may read LastLine or the
// process's Result without wedging the child's output.
func tell(onLine func(string), lines []string) {
	if onLine == nil {
		return
	}
	for _, line := range lines {
		onLine(line)
	}
}

// emitLocked logs one line unless it is noise, and returns it cleaned of
// colour codes, or empty for a blank line.
func (w *LineWriter) emitLocked(line string) string {
	line = strings.TrimSpace(ansiColour.ReplaceAllString(strings.TrimRight(line, "\r"), ""))
	if line == "" {
		return ""
	}
	now := w.now()
	w.reportFoldedLocked(now, false)
	if w.foldLocked(line, now) {
		return line
	}
	w.lastLine = line
	logger.Infof("%s | %s", w.label, line)
	return line
}

func (w *LineWriter) foldLocked(line string, now time.Time) bool {
	if r := w.folding; r != nil && r.Continuation != nil && r.Continuation.MatchString(line) {
		return true
	}
	w.folding = nil
	for i := range w.noise {
		r := &w.noise[i]
		if !r.Line.MatchString(line) {
			continue
		}
		if r.Silent {
			return true
		}
		if len(w.folded) == 0 {
			w.since = now
		}
		w.folded[r.Kind]++
		w.folding = r
		return true
	}
	return false
}

// reportFoldedLocked writes the count once the window is over, or at once
// when final, so a process that exits does not take its count with it.
func (w *LineWriter) reportFoldedLocked(now time.Time, final bool) {
	if len(w.folded) == 0 {
		return
	}
	elapsed := now.Sub(w.since)
	if !final && elapsed < foldWindow {
		return
	}
	total := 0
	var kinds []string
	for _, r := range w.noise {
		n, ok := w.folded[r.Kind]
		if !ok {
			continue
		}
		total += n
		kinds = append(kinds, fmt.Sprintf("%s %d", r.Kind, n))
		// Deleted as printed, so two rules sharing a kind print it once.
		delete(w.folded, r.Kind)
	}
	logger.Infof("%s | suppressed %d probe messages in %s: %s", w.label, total, foldSpan(elapsed), strings.Join(kinds, ", "))
	clear(w.folded)
}

func foldSpan(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return fmt.Sprintf("%dm", int(d/time.Minute))
}

func (w *LineWriter) LastLine() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastLine
}
