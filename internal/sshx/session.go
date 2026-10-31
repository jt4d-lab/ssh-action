package sshx

import (
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	ptyTerm   = "xterm"
	ptyHeight = 40
	ptyWidth  = 80
)

// terminalModes repeat the modes easyssh-proxy requests for request_pty: no
// echo, with the classic 14.4 kbaud speeds.
var terminalModes = ssh.TerminalModes{
	ssh.ECHO:          0,
	ssh.TTY_OP_ISPEED: 14400,
	ssh.TTY_OP_OSPEED: 14400,
}

// sessionSpec is one command to run in a new session over a client.
type sessionSpec struct {
	name    string // host name, the first word of every error
	label   string // the step running the command, the second word of every error
	command string
	pty     bool
	stdout  io.Writer                  // where the remote stdout goes
	stderr  io.Writer                  // where the remote stderr goes
	feed    func(io.WriteCloser) error // writes the stdin of a command that reads one
}

// sessionResult is the outcome of a finished command for the caller to classify
// into the error of its step.
type sessionResult struct {
	waitErr  error
	flushErr error
	feedErr  error
	expired  bool
}

// runSession runs spec.command in a new session on client and returns its
// outcome. A deadline that has passed reports "<label> timeout" without opening
// a session; one that passes while the command runs closes client, which is the
// only way to unblock a hung command. The writers receive complete lines.
func runSession(client *ssh.Client, spec sessionSpec, deadline time.Time) (sessionResult, error) {
	timer, ok := armDeadline(client, deadline)
	if !ok {
		return sessionResult{}, fmt.Errorf("host %s: %s timeout", spec.name, spec.label)
	}
	defer timer.stop()

	sess, err := client.NewSession()
	if err != nil {
		return sessionResult{}, fmt.Errorf("host %s: %s: %w", spec.name, spec.label, err)
	}
	defer func() { _ = sess.Close() }()

	if spec.pty {
		if err := sess.RequestPty(ptyTerm, ptyHeight, ptyWidth, terminalModes); err != nil {
			return sessionResult{}, fmt.Errorf("host %s: %s: pty: %w", spec.name, spec.label, err)
		}
	}

	out := newLineWriter(spec.stdout)
	errOut := newLineWriter(spec.stderr)
	sess.Stdout = out
	sess.Stderr = errOut

	var stdin io.WriteCloser
	if spec.feed != nil {
		if stdin, err = sess.StdinPipe(); err != nil {
			return sessionResult{}, fmt.Errorf("host %s: %s: %w", spec.name, spec.label, err)
		}
	}

	if err := sess.Start(spec.command); err != nil {
		return sessionResult{}, fmt.Errorf("host %s: %s: %w", spec.name, spec.label, err)
	}

	var feedErr error
	if spec.feed != nil {
		feedErr = spec.feed(stdin)
		if err := stdin.Close(); err != nil && feedErr == nil {
			feedErr = err
		}
	}

	waitErr := sess.Wait()
	return sessionResult{
		waitErr:  waitErr,
		flushErr: errors.Join(out.Flush(), errOut.Flush()),
		feedErr:  feedErr,
		expired:  timer.expired(),
	}, nil
}
