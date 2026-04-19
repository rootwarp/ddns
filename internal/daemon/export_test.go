package daemon

import "time"

// SetClock overrides the daemon's internal clock for tests. It lives in
// export_test.go so production callers cannot reach into the daemon to
// swap time — the knob exists only under _test.go builds.
func SetClock(d *Daemon, clock func() time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clock = clock
}

// UpdateBackoff exposes the unexported updateBackoff for tests that want
// to exercise the backoff state machine without driving a full tick
// through Run. The behavior is identical to the internal call.
func UpdateBackoff(d *Daemon, err error) error {
	return d.updateBackoff(err)
}

// ConsecutiveFailures returns the current transient-backoff counter.
// Tests use it to confirm the counter advances or resets across a
// sequence of updateBackoff calls.
func ConsecutiveFailures(d *Daemon) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.backoff.consecutiveFailures
}

// ConsecutiveFatalFailures returns Fix M's fatal counter (ErrAuth/ErrConfig).
func ConsecutiveFatalFailures(d *Daemon) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.backoff.consecutiveFatalFailures
}

// ConfigPollInterval returns the current live PollInterval under the
// read lock. Used by issue 4.4 tests to confirm UpdateConfig actually
// swapped the value.
func ConfigPollInterval(d *Daemon) time.Duration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cfg.PollInterval
}
