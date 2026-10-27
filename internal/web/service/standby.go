package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
)

// A standby panel is a copy kept ready on another server. Its web panel, Xray
// and sidecars run; whatever would clash with the main panel stays off.

// standbyRestartDelay lets the request that changed the mode be answered first.
const standbyRestartDelay = time.Second

// restartPanelOnly restarts the panel process and leaves Xray running; a test stands in for it.
var restartPanelOnly = func() {
	if !global.TriggerRestart() {
		logger.Warning("standby: no restart hook; the new mode takes effect at the next start")
	}
}

// StandbyMarked reports whether the standby marker is on disk. One that cannot
// be checked counts as there: a copy must never wake up as the main panel.
func StandbyMarked() (bool, error) {
	_, err := os.Stat(config.StandbyMarkerPath())
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return true, err
	}
}

// WriteStandbyMarker puts the marker down or takes it away, and nothing else.
func WriteStandbyMarker(on bool) error {
	path := config.StandbyMarkerPath()
	if !on {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte("this panel is a standby copy; `x-ui standby off` makes it the main one\n"), 0o600)
}

// SetStandbyMode puts this panel on standby or makes it the main one, then
// restarts the panel process so every job and bot starts again under the new mode.
func SetStandbyMode(on bool) error {
	if err := WriteStandbyMarker(on); err != nil {
		return fmt.Errorf("write the standby marker: %w", err)
	}
	global.SetStandby(on)
	var xrayErr error
	if on {
		(&RelayService{}).StandDown()
	} else {
		xrayErr = ensureXrayRunning()
	}
	time.AfterFunc(standbyRestartDelay, restartPanelOnly)
	return xrayErr
}

// ensureXrayRunning starts Xray when it is not running; a running one is left
// alone, so becoming the main panel drops no customer.
func ensureXrayRunning() error {
	xs := &XrayService{}
	if xs.IsXrayRunning() {
		return nil
	}
	if err := xs.RestartXray(true); err != nil {
		return fmt.Errorf("this panel is the main one now, but Xray did not start: %w", err)
	}
	return nil
}
