package sshx

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jt4d-lab/ssh-action/internal/config"
	"github.com/jt4d-lab/ssh-action/internal/script"
)

// upload copies entries into dir on host over a single session that runs
// "mkdir -p <dir> && tar -C <dir> -xf -" and reads the tar stream of writeTar
// from stdin. The remote stderr goes to stderr, warnings about skipped local
// entries go to warn. The ssh client writes the remote stderr while writeTar
// reports the skipped entries, so upload serializes those writes and the two
// writers may be the same one. deadline bounds the whole upload: when it passes,
// client is closed, which is what unblocks a stuck transfer, and the error
// reports an upload timeout.
func upload(client *ssh.Client, host config.Host, dir string, entries []string, deadline time.Time, stderr, warn io.Writer) error {
	name := hostName(host)

	lock := &sync.Mutex{}
	warn = &lockedWriter{mu: lock, w: warn}

	target, err := script.QuotePath(dir)
	if err != nil {
		return fmt.Errorf("host %s: upload: %w", name, err)
	}

	res, err := runSession(client, sessionSpec{
		name:    name,
		label:   "upload",
		command: "mkdir -p " + target + " && tar -C " + target + " -xf -",
		stdout:  io.Discard,
		stderr:  &lockedWriter{mu: lock, w: stderr},
		feed: func(stdin io.WriteCloser) error {
			return writeTar(stdin, entries, warn)
		},
	}, deadline)
	if err != nil {
		return err
	}

	if res.expired {
		return errors.Join(fmt.Errorf("host %s: upload timeout", name), res.flushErr)
	}

	// A tar that dies early closes the channel under the writer, so a local
	// stream error and a remote exit status can describe the same failure; both
	// are reported because either one may be the cause.
	var errs []error
	if res.feedErr != nil {
		errs = append(errs, fmt.Errorf("host %s: upload to %s: %w", name, dir, res.feedErr))
	}
	if res.flushErr != nil {
		errs = append(errs, fmt.Errorf("host %s: upload: write stderr: %w", name, res.flushErr))
	}
	var exitErr *ssh.ExitError
	switch {
	case errors.As(res.waitErr, &exitErr):
		errs = append(errs, fmt.Errorf("host %s: upload to %s: tar exited with status %d", name, dir, exitErr.ExitStatus()))
	case res.waitErr != nil:
		errs = append(errs, fmt.Errorf("host %s: upload to %s: %w", name, dir, res.waitErr))
	}
	return errors.Join(errs...)
}

// lockedWriter serializes the writes of goroutines that share one writer.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}
