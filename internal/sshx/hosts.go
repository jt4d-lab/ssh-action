package sshx

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jt4d-lab/ssh-action/internal/config"
)

// successLine is the summary written once every host succeeded.
const successLine = "✅ Successfully executed commands to all hosts."

// Run executes the steps of every host: connect, upload entries into
// cfg.Workdir when entries is not empty, then run scriptText. The steps of one
// host share a single deadline of cfg.CommandTimeout, and a failed upload leaves
// the script of that host unstarted. Remote stdout and stderr pass through
// unprefixed for a single host and prefixed with "host: " for several hosts.
//
// Hosts run concurrently by default: all of them start, and the first error in
// hosts order is returned once every host finished. With cfg.Sync they run one
// at a time and the first error stops the run. successLine is written to stderr
// when the run succeeds.
func Run(cfg *config.Config, hosts []config.Host, scriptText string, entries []string, stdout, stderr io.Writer) error {
	prefix := len(hosts) > 1
	outMu, errMu := &sync.Mutex{}, &sync.Mutex{}
	streams := func(host config.Host) (io.Writer, io.Writer) {
		if !prefix {
			return stdout, stderr
		}
		p := []byte(hostName(host) + ": ")
		return &prefixWriter{mu: outMu, w: stdout, prefix: p}, &prefixWriter{mu: errMu, w: stderr, prefix: p}
	}

	if cfg.Sync {
		for _, host := range hosts {
			out, errOut := streams(host)
			if err := runHost(cfg, host, scriptText, entries, out, errOut); err != nil {
				return err
			}
		}
	} else {
		errs := make([]error, len(hosts))
		var wg sync.WaitGroup
		for i, host := range hosts {
			out, errOut := streams(host)
			wg.Go(func() {
				errs[i] = runHost(cfg, host, scriptText, entries, out, errOut)
			})
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return err
			}
		}
	}

	_, _ = fmt.Fprintln(stderr, successLine)
	return nil
}

// runHost runs the steps of one host under a single command deadline.
func runHost(cfg *config.Config, host config.Host, scriptText string, entries []string, stdout, stderr io.Writer) error {
	deadline := time.Now().Add(cfg.CommandTimeout)

	client, err := dial(cfg, host)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	if len(entries) > 0 {
		if err := upload(client, host, cfg.Workdir, entries, deadline, stderr, stderr); err != nil {
			return err
		}
	}
	return execScript(client, host, scriptText, cfg.RequestPty, deadline, stdout, stderr)
}

// prefixWriter writes prefix and then one write of a host. The hosts of a run
// share both the underlying writer and the mutex, so the prefix and the line it
// belongs to reach the log together.
type prefixWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.w.Write(p.prefix); err != nil {
		return 0, err
	}
	if _, err := p.w.Write(b); err != nil {
		return 0, err
	}
	return len(b), nil
}
