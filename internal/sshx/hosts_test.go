package sshx

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jt4d-lab/ssh-action/internal/config"
	"github.com/jt4d-lab/ssh-action/internal/sshtest"
)

// multiHosts returns the config and the hosts that point at the servers
// listening on addrs.
func multiHosts(t *testing.T, addrs ...string) (*config.Config, []config.Host) {
	t.Helper()
	list := strings.Join(addrs, ",")
	cfg, _ := testTarget(t, addrs[0], testPassword)
	hosts, err := config.ParseHosts(list, config.DefaultPort, config.DefaultProtocol)
	if err != nil {
		t.Fatalf("ParseHosts(%q): %v", list, err)
	}
	cfg.Host = list
	cfg.CommandTimeout = testDeadline
	return cfg, hosts
}

func TestRunHostsInParallel(t *testing.T) {
	barrier := sshtest.NewBarrier(2)
	handler := func(sess *sshtest.Session) int {
		if err := barrier.Wait(time.Second); err != nil {
			_, _ = fmt.Fprintf(sess.Stderr, "%v\n", err)
			return 1
		}
		_, _ = io.WriteString(sess.Stdout, "ran\n")
		return 0
	}
	first := startServer(t, handler)
	second := startServer(t, handler)
	cfg, hosts := multiHosts(t, first.Addr(), second.Addr())

	var stdout, stderr bytes.Buffer
	if err := Run(cfg, hosts, "ignored", nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v (stderr %q)", err, stderr.String())
	}
	if got := strings.Count(stdout.String(), "ran\n"); got != 2 {
		t.Errorf("stdout = %q, want both hosts to run", stdout.String())
	}
}

// echoHandler records every session and writes its command to stdout.
func echoHandler(rec *recorder) sshtest.Handler {
	return func(sess *sshtest.Session) int {
		rec.record(sess)
		_, _ = io.WriteString(sess.Stdout, sess.Command+"\n")
		return 0
	}
}

// TestRunParallelReportsTheFirstHostError lets the second host fail before the
// first one and pins that the returned error belongs to the first host in hosts
// order and that both hosts ran their step.
func TestRunParallelReportsTheFirstHostError(t *testing.T) {
	started := make(chan struct{}, 1)
	first := startServer(t, func(*sshtest.Session) int {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
		}
		return 5
	})
	rec := &recorder{}
	second := startServer(t, func(sess *sshtest.Session) int {
		rec.record(sess)
		started <- struct{}{}
		return 3
	})
	cfg, hosts := multiHosts(t, first.Addr(), second.Addr())

	var stdout, stderr bytes.Buffer
	err := Run(cfg, hosts, "ignored", nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("Run with a failing host succeeded")
	}
	for _, want := range []string{hostName(hosts[0]), "status 5"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if sessions := rec.all(); len(sessions) != 1 {
		t.Errorf("the second host ran %d sessions, want it to finish its step", len(sessions))
	}
	if strings.Contains(stderr.String(), successLine) {
		t.Errorf("stderr = %q, want no summary line after a failure", stderr.String())
	}
}

func TestRunSyncRunsEveryHost(t *testing.T) {
	firstRec, secondRec := &recorder{}, &recorder{}
	first := startServer(t, echoHandler(firstRec))
	second := startServer(t, echoHandler(secondRec))
	cfg, hosts := multiHosts(t, first.Addr(), second.Addr())
	cfg.Sync = true

	var stdout, stderr bytes.Buffer
	if err := Run(cfg, hosts, "echo synced", nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v (stderr %q)", err, stderr.String())
	}
	for i, rec := range []*recorder{firstRec, secondRec} {
		if sessions := rec.all(); len(sessions) != 1 {
			t.Errorf("host %d ran %d sessions, want one script", i, len(sessions))
		}
	}
	if got := strings.Count(stdout.String(), "synced\n"); got != 2 {
		t.Errorf("stdout = %q, want both hosts to run the script", stdout.String())
	}
	if got := strings.Count(stderr.String(), successLine); got != 1 {
		t.Errorf("summary line appears %d times in %q, want once", got, stderr.String())
	}
}

func TestRunSyncStopsAtFirstFailure(t *testing.T) {
	failing := startServer(t, func(*sshtest.Session) int { return 3 })
	rec := &recorder{}
	next := startServer(t, func(sess *sshtest.Session) int {
		rec.record(sess)
		return 0
	})
	cfg, hosts := multiHosts(t, failing.Addr(), next.Addr())
	cfg.Sync = true

	var stdout, stderr bytes.Buffer
	err := Run(cfg, hosts, "ignored", nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("Run with a failing host succeeded")
	}
	for _, want := range []string{hostName(hosts[0]), "status 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if sessions := rec.all(); len(sessions) != 0 {
		t.Errorf("the host after the failure ran: %v", sessions)
	}
	if strings.Contains(stderr.String(), successLine) {
		t.Errorf("stderr = %q, want no summary line after a failure", stderr.String())
	}
}

func TestRunOutputPrefix(t *testing.T) {
	const script = "echo out; echo err >&2"

	t.Run("single host", func(t *testing.T) {
		srv := startServer(t, sshtest.ShellHandler())
		cfg, hosts := multiHosts(t, srv.Addr())

		var stdout, stderr bytes.Buffer
		if err := Run(cfg, hosts, script, nil, &stdout, &stderr); err != nil {
			t.Fatalf("Run: %v (stderr %q)", err, stderr.String())
		}
		if got, want := stdout.String(), "out\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}
		if !strings.Contains(stderr.String(), "err\n") {
			t.Errorf("stderr = %q, want the remote stderr", stderr.String())
		}
		if !strings.Contains(stderr.String(), successLine+"\n") {
			t.Errorf("stderr = %q, want the summary line", stderr.String())
		}
		if strings.Contains(stderr.String(), hostName(hosts[0])) {
			t.Errorf("stderr = %q, want no host prefix for a single host", stderr.String())
		}
	})

	t.Run("several hosts", func(t *testing.T) {
		first := startServer(t, sshtest.ShellHandler())
		second := startServer(t, sshtest.ShellHandler())
		cfg, hosts := multiHosts(t, first.Addr(), second.Addr())

		var stdout, stderr bytes.Buffer
		if err := Run(cfg, hosts, script, nil, &stdout, &stderr); err != nil {
			t.Fatalf("Run: %v (stderr %q)", err, stderr.String())
		}
		for _, host := range hosts {
			if want := hostName(host) + ": out\n"; !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want the line %q", stdout.String(), want)
			}
			if want := hostName(host) + ": err\n"; !strings.Contains(stderr.String(), want) {
				t.Errorf("stderr = %q, want the line %q", stderr.String(), want)
			}
		}
		if got := strings.Count(stderr.String(), successLine+"\n"); got != 1 {
			t.Errorf("summary line appears %d times in %q, want once", got, stderr.String())
		}
	})
}

func TestRunUploadsBeforeScript(t *testing.T) {
	local := localTree(t)
	entries, err := MatchFiles(filepath.Join(local, "app"), nil)
	if err != nil {
		t.Fatalf("MatchFiles: %v", err)
	}

	t.Run("success", func(t *testing.T) {
		hostDir := t.TempDir()
		rec := &recorder{}
		srv := startServer(t, treeHandler(hostDir, rec))
		cfg, hosts := multiHosts(t, srv.Addr())
		cfg.Workdir = "/srv/app"

		const script = "echo script ran"
		var stdout bytes.Buffer
		if err := Run(cfg, hosts, script, entries, &stdout, io.Discard); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got, want := readFile(t, filepath.Join(hostDir, "app", "config.yml")), "port: 8080\n"; got != want {
			t.Errorf("config.yml = %q, want %q", got, want)
		}
		if !strings.Contains(stdout.String(), "script ran\n") {
			t.Errorf("stdout = %q, want the script output", stdout.String())
		}

		sessions := rec.all()
		if len(sessions) != 2 {
			t.Fatalf("sessions = %v, want an upload and a script", sessions)
		}
		if want := uploadCommandPrefix + "'/srv/app'"; !strings.HasPrefix(sessions[0].command, want) {
			t.Errorf("first session command = %q, want an upload to %q", sessions[0].command, cfg.Workdir)
		}
		if sessions[1].command != script {
			t.Errorf("second session command = %q, want %q", sessions[1].command, script)
		}
	})

	t.Run("every host receives the entries", func(t *testing.T) {
		firstDir, secondDir := t.TempDir(), t.TempDir()
		firstRec, secondRec := &recorder{}, &recorder{}
		first := startServer(t, treeHandler(firstDir, firstRec))
		second := startServer(t, treeHandler(secondDir, secondRec))
		cfg, hosts := multiHosts(t, first.Addr(), second.Addr())
		cfg.Workdir = "/srv/app"

		if err := Run(cfg, hosts, "echo script ran", entries, io.Discard, io.Discard); err != nil {
			t.Fatalf("Run: %v", err)
		}
		for i, dir := range []string{firstDir, secondDir} {
			if got, want := readFile(t, filepath.Join(dir, "app", "config.yml")), "port: 8080\n"; got != want {
				t.Errorf("host %d config.yml = %q, want %q", i, got, want)
			}
		}
		for i, rec := range []*recorder{firstRec, secondRec} {
			if sessions := rec.all(); len(sessions) != 2 {
				t.Errorf("host %d ran %v, want an upload and a script", i, sessions)
			}
		}
	})

	t.Run("upload failure skips script", func(t *testing.T) {
		rec := &recorder{}
		srv := startServer(t, func(sess *sshtest.Session) int {
			rec.record(sess)
			_, _ = io.WriteString(sess.Stderr, "tar: broken\n")
			return 1
		})
		cfg, hosts := multiHosts(t, srv.Addr())
		cfg.Workdir = "/srv/app"

		err := Run(cfg, hosts, "echo script ran", entries, io.Discard, io.Discard)
		if err == nil {
			t.Fatal("Run with a failing upload succeeded")
		}
		if !strings.Contains(err.Error(), "status 1") {
			t.Errorf("error %q does not report the tar exit status", err)
		}
		if sessions := rec.all(); len(sessions) != 1 {
			t.Errorf("sessions = %v, want only the failed upload", sessions)
		}
	})
}
