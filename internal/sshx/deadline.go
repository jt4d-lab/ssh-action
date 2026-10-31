package sshx

import (
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// deadlineTimer bounds one step of a host: when the time left until its deadline
// runs out, it closes the client, which is the only way to unblock a stuck
// transfer or command.
type deadlineTimer struct {
	timedOut atomic.Bool
	timer    *time.Timer
}

// armDeadline starts the timer that closes client at deadline. It reports false
// instead when deadline has already passed, and then arms nothing.
func armDeadline(client *ssh.Client, deadline time.Time) (*deadlineTimer, bool) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, false
	}
	guard := &deadlineTimer{}
	guard.timer = time.AfterFunc(remaining, func() {
		guard.timedOut.Store(true)
		_ = client.Close()
	})
	return guard, true
}

// stop cancels the timer.
func (d *deadlineTimer) stop() { d.timer.Stop() }

// expired reports whether the timer closed the client.
func (d *deadlineTimer) expired() bool { return d.timedOut.Load() }
