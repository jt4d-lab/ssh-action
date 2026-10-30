package sshx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jt4d-lab/ssh-action/internal/config"
)

// execScript runs script on host over client, writing the remote stdout to
// stdout and the remote stderr to stderr line by line. A non-zero remote exit
// status is reported as an error naming the host and the status. When deadline
// passes before the command returns, the error reports a command timeout and
// client is closed, which is the only way to unblock a hung command.
func execScript(client *ssh.Client, host config.Host, script string, requestPty bool, deadline time.Time, stdout, stderr io.Writer) error {
	name := hostName(host)
	res, err := runSession(client, sessionSpec{
		name:    name,
		label:   "command",
		command: script,
		pty:     requestPty,
		stdout:  stdout,
		stderr:  stderr,
	}, deadline)
	if err != nil {
		return err
	}

	switch {
	case res.expired:
		return errors.Join(fmt.Errorf("host %s: command timeout", name), res.flushErr)
	case res.flushErr != nil:
		return fmt.Errorf("host %s: command: write output: %w", name, res.flushErr)
	}

	var exitErr *ssh.ExitError
	if errors.As(res.waitErr, &exitErr) {
		return fmt.Errorf("host %s: command exited with status %d", name, exitErr.ExitStatus())
	}
	if res.waitErr != nil {
		return fmt.Errorf("host %s: command: %w", name, res.waitErr)
	}
	return nil
}

// lineWriter writes remote output one line at a time: a complete line reaches w
// as soon as its newline arrives, and Flush writes the last line the remote
// command left without a trailing newline.
type lineWriter struct {
	w   io.Writer
	buf []byte
}

func newLineWriter(w io.Writer) *lineWriter {
	return &lineWriter{w: w}
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		if _, err := l.w.Write(l.buf[:i+1]); err != nil {
			return 0, err
		}
		l.buf = l.buf[i+1:]
	}
}

func (l *lineWriter) Flush() error {
	if len(l.buf) == 0 {
		return nil
	}
	l.buf = append(l.buf, '\n')
	_, err := l.w.Write(l.buf)
	l.buf = nil
	return err
}
