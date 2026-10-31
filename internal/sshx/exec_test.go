package sshx

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jt4d-lab/ssh-action/internal/config"
	"github.com/jt4d-lab/ssh-action/internal/sshtest"
)

const (
	testUser     = "tester"
	testPassword = "s3cret"

	// testDeadline is far enough away for a test command to finish.
	testDeadline = 10 * time.Second
)

func startServer(t *testing.T, handler sshtest.Handler) *sshtest.Server {
	t.Helper()
	srv, err := sshtest.NewServer(sshtest.Options{Password: testPassword, Handler: handler})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// testTarget returns the config and host that point at the server listening on
// addr.
func testTarget(t *testing.T, addr, password string) (*config.Config, config.Host) {
	t.Helper()
	hosts, err := config.ParseHosts(addr, config.DefaultPort, config.DefaultProtocol)
	if err != nil {
		t.Fatalf("ParseHosts(%q): %v", addr, err)
	}
	cfg := &config.Config{
		Host:     addr,
		Port:     config.DefaultPort,
		Protocol: config.DefaultProtocol,
		Username: testUser,
		Password: password,
		Timeout:  5 * time.Second,
	}
	return cfg, hosts[0]
}

func connectTo(t *testing.T, cfg *config.Config, host config.Host) *ssh.Client {
	t.Helper()
	client, err := dial(cfg, host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestExecScriptStreamsLinesToStdoutAndStderr(t *testing.T) {
	srv := startServer(t, sshtest.ShellHandler())
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	var stdout, stderr bytes.Buffer
	script := "printf 'out one\\nout two\\npartial'; printf 'err one\\n' >&2"
	err := execScript(client, host, script, false, time.Now().Add(testDeadline), &stdout, &stderr)
	if err != nil {
		t.Fatalf("execScript: %v", err)
	}

	if got, want := stdout.String(), "out one\nout two\npartial\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "err one\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

func TestExecScriptStreamsBeforeCommandExit(t *testing.T) {
	seen := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := startServer(t, func(sess *sshtest.Session) int {
		_, _ = io.WriteString(sess.Stdout, "first\n")
		<-release
		_, _ = io.WriteString(sess.Stdout, "second\n")
		return 0
	})
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	out := &notifyingWriter{seen: seen}
	done := make(chan error, 1)
	go func() {
		done <- execScript(client, host, "ignored", false, time.Now().Add(testDeadline), out, io.Discard)
	}()

	select {
	case <-seen:
	case <-time.After(testDeadline):
		close(release)
		t.Fatalf("no output arrived while the command was still running")
	}
	close(release)

	if err := <-done; err != nil {
		t.Fatalf("execScript: %v", err)
	}
	if got, want := out.String(), "first\nsecond\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestDialWrongPassword(t *testing.T) {
	srv := startServer(t, sshtest.ShellHandler())
	cfg, host := testTarget(t, srv.Addr(), "wrong")

	_, err := dial(cfg, host)
	if err == nil {
		t.Fatal("dial with a wrong password succeeded")
	}
	if !strings.Contains(err.Error(), hostName(host)) {
		t.Errorf("error %q does not name the host %q", err, hostName(host))
	}
	if !strings.Contains(err.Error(), "unable to authenticate") {
		t.Errorf("error %q does not report failed authentication", err)
	}
}

func TestDialFingerprint(t *testing.T) {
	srv := startServer(t, sshtest.ShellHandler())
	cfg, host := testTarget(t, srv.Addr(), testPassword)

	cfg.Fingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := dial(cfg, host); err == nil {
		t.Fatal("dial with a wrong fingerprint succeeded")
	} else if !strings.Contains(err.Error(), "fingerprint") {
		t.Errorf("error %q does not report the fingerprint mismatch", err)
	}

	cfg.Fingerprint = ssh.FingerprintSHA256(srv.HostKey())
	client := connectTo(t, cfg, host)
	if _, err := client.NewSession(); err != nil {
		t.Fatalf("NewSession after a matching fingerprint: %v", err)
	}
}

func TestExecScriptNonZeroExit(t *testing.T) {
	srv := startServer(t, func(sess *sshtest.Session) int { return 7 })
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	err := execScript(client, host, "ignored", false, time.Now().Add(testDeadline), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("execScript of a failing command succeeded")
	}
	if !strings.Contains(err.Error(), hostName(host)) {
		t.Errorf("error %q does not name the host %q", err, hostName(host))
	}
	if !strings.Contains(err.Error(), "status 7") {
		t.Errorf("error %q does not report the remote exit status", err)
	}
}

func TestExecScriptCommandTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := startServer(t, func(sess *sshtest.Session) int {
		<-release
		return 0
	})
	t.Cleanup(func() { close(release) })
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	t.Run("running command", func(t *testing.T) {
		start := time.Now()
		err := execScript(client, host, "ignored", false, time.Now().Add(100*time.Millisecond), io.Discard, io.Discard)
		if err == nil {
			t.Fatal("execScript of a hung command succeeded")
		}
		if !strings.Contains(err.Error(), "command timeout") {
			t.Errorf("error %q does not report a command timeout", err)
		}
		if elapsed := time.Since(start); elapsed > testDeadline {
			t.Errorf("timeout took %s", elapsed)
		}
		if _, err := client.NewSession(); err == nil {
			t.Error("client is still open after the command timeout")
		}
	})

	t.Run("expired deadline", func(t *testing.T) {
		err := execScript(client, host, "ignored", false, time.Now().Add(-time.Second), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "command timeout") {
			t.Errorf("error = %v, want a command timeout", err)
		}
	})
}

func TestExecScriptRequestPty(t *testing.T) {
	type request struct {
		command string
		pty     *sshtest.Pty
	}
	requests := make(chan request, 1)
	srv := startServer(t, func(sess *sshtest.Session) int {
		requests <- request{command: sess.Command, pty: sess.Pty}
		_, _ = io.WriteString(sess.Stdout, "ok\n")
		return 0
	})
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	var stdout bytes.Buffer
	const script = "echo pty"
	if err := execScript(client, host, script, true, time.Now().Add(testDeadline), &stdout, io.Discard); err != nil {
		t.Fatalf("execScript: %v", err)
	}

	got := <-requests
	if got.command != script {
		t.Errorf("remote command = %q, want %q", got.command, script)
	}
	if got.pty == nil {
		t.Fatal("the script session got no pty")
	}
	if got.pty.Term != ptyTerm {
		t.Errorf("pty term = %q, want %q", got.pty.Term, ptyTerm)
	}
	if stdout.String() != "ok\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "ok\n")
	}
}

func TestExecScriptWithoutPty(t *testing.T) {
	fetched := make(chan *sshtest.Pty, 1)
	srv := startServer(t, func(sess *sshtest.Session) int {
		fetched <- sess.Pty
		return 0
	})
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	if err := execScript(client, host, "echo plain", false, time.Now().Add(testDeadline), io.Discard, io.Discard); err != nil {
		t.Fatalf("execScript: %v", err)
	}
	if pty := <-fetched; pty != nil {
		t.Errorf("pty = %+v, want none without request_pty", pty)
	}
}

// notifyingWriter reports the first write on seen and keeps everything written
// for later inspection.
type notifyingWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	seen chan struct{}
	once sync.Once
}

func (w *notifyingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.once.Do(func() { close(w.seen) })
	return w.buf.Write(p)
}

func (w *notifyingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
