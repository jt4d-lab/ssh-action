package sshx

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jt4d-lab/ssh-action/internal/config"
	"github.com/jt4d-lab/ssh-action/internal/script"
)

// upload copies entries into dir on host over a single session that runs
// "mkdir -p <dir> && tar -C <dir> -xf -" and reads the tar stream of writeTar
// from stdin. The remote stderr goes to stderr, warnings about skipped local
// entries go to warn. deadline bounds the whole upload: when it passes, client
// is closed, which is what unblocks a stuck transfer, and the error reports an
// upload timeout.
func upload(client *ssh.Client, host config.Host, dir string, entries []string, deadline time.Time, stderr, warn io.Writer) error {
	name := hostName(host)
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return fmt.Errorf("host %s: upload timeout", name)
	}

	target, err := script.QuotePath(dir)
	if err != nil {
		return fmt.Errorf("host %s: upload: %w", name, err)
	}

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("host %s: upload: %w", name, err)
	}
	defer func() { _ = session.Close() }()

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("host %s: upload: %w", name, err)
	}

	errOut := newLineWriter(stderr)
	session.Stderr = errOut

	if err := session.Start("mkdir -p " + target + " && tar -C " + target + " -xf -"); err != nil {
		return fmt.Errorf("host %s: upload to %s: %w", name, dir, err)
	}

	var timedOut atomic.Bool
	timer := time.AfterFunc(remaining, func() {
		timedOut.Store(true)
		_ = client.Close()
	})

	streamErr := writeTar(stdin, entries, warn)
	if err := stdin.Close(); err != nil && streamErr == nil {
		streamErr = err
	}
	waitErr := session.Wait()
	timer.Stop()

	flushErr := errOut.Flush()
	if timedOut.Load() {
		return errors.Join(fmt.Errorf("host %s: upload timeout", name), flushErr)
	}

	// A tar that dies early closes the channel under the writer, so a local
	// stream error and a remote exit status can describe the same failure; both
	// are reported because either one may be the cause.
	var errs []error
	if streamErr != nil {
		errs = append(errs, fmt.Errorf("host %s: upload to %s: %w", name, dir, streamErr))
	}
	if flushErr != nil {
		errs = append(errs, fmt.Errorf("host %s: upload: write stderr: %w", name, flushErr))
	}
	var exitErr *ssh.ExitError
	switch {
	case errors.As(waitErr, &exitErr):
		errs = append(errs, fmt.Errorf("host %s: upload to %s: tar exited with status %d", name, dir, exitErr.ExitStatus()))
	case waitErr != nil:
		errs = append(errs, fmt.Errorf("host %s: upload to %s: %w", name, dir, waitErr))
	}
	return errors.Join(errs...)
}
