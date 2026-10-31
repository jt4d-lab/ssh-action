package sshtest

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	testUser     = "tester"
	testPassword = "s3cret"
	testTimeout  = 5 * time.Second
)

func startServer(t *testing.T, opts Options) *Server {
	t.Helper()
	srv, err := NewServer(opts)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func clientConfig(password string) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            testUser,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         testTimeout,
	}
}

func dial(t *testing.T, srv *Server, config *ssh.ClientConfig) *ssh.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", srv.Addr(), config)
	if err != nil {
		t.Fatalf("ssh.Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func runCommand(client *ssh.Client, command string) (string, string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", "", err
	}
	defer func() { _ = sess.Close() }()

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	err = sess.Run(command)
	return stdout.String(), stderr.String(), err
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error %v is not an *ssh.ExitError", err)
	}
	return exitErr.ExitStatus()
}

func TestServerPasswordAuth(t *testing.T) {
	srv := startServer(t, Options{Password: testPassword, Handler: ShellHandler()})

	client := dial(t, srv, clientConfig(testPassword))
	if _, _, err := runCommand(client, "exit 0"); err != nil {
		t.Fatalf("authenticated command failed: %v", err)
	}

	_, err := ssh.Dial("tcp", srv.Addr(), clientConfig("wrong"))
	if err == nil {
		t.Fatal("dial with a wrong password succeeded")
	}
	if !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("wrong password error = %v", err)
	}
}

func TestServerPublicKeyAuth(t *testing.T) {
	clientKey := newSigner(t)
	strangerKey := newSigner(t)

	srv := startServer(t, Options{AuthorizedKeys: []ssh.PublicKey{clientKey.PublicKey()}, Handler: ShellHandler()})

	config := &ssh.ClientConfig{
		User:            testUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientKey)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         testTimeout,
	}
	client := dial(t, srv, config)
	if _, _, err := runCommand(client, "exit 0"); err != nil {
		t.Fatalf("authenticated command failed: %v", err)
	}

	strangerConfig := &ssh.ClientConfig{
		User:            testUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(strangerKey)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         testTimeout,
	}
	if _, err := ssh.Dial("tcp", srv.Addr(), strangerConfig); err == nil {
		t.Fatal("dial with an unauthorized key succeeded")
	}
}

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer
}

func TestServerHostKey(t *testing.T) {
	srv := startServer(t, Options{Password: testPassword, Handler: ShellHandler()})

	var presented ssh.PublicKey
	config := clientConfig(testPassword)
	config.HostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
		presented = key
		return nil
	}
	dial(t, srv, config)

	if presented == nil {
		t.Fatal("host key callback was not called")
	}
	if got, want := ssh.FingerprintSHA256(presented), ssh.FingerprintSHA256(srv.HostKey()); got != want {
		t.Fatalf("presented fingerprint = %s, want %s", got, want)
	}
}

func TestServerExecStreams(t *testing.T) {
	srv := startServer(t, Options{Password: testPassword, Handler: func(sess *Session) int {
		_, _ = io.WriteString(sess.Stdout, "out line\n")
		_, _ = io.WriteString(sess.Stderr, "err line\n")
		return 3
	}})

	client := dial(t, srv, clientConfig(testPassword))
	stdout, stderr, err := runCommand(client, "whatever")

	if stdout != "out line\n" {
		t.Errorf("stdout = %q, want %q", stdout, "out line\n")
	}
	if stderr != "err line\n" {
		t.Errorf("stderr = %q, want %q", stderr, "err line\n")
	}
	if code := exitCode(t, err); code != 3 {
		t.Errorf("exit status = %d, want 3", code)
	}
}

func TestServerExecStdin(t *testing.T) {
	payload := strings.Repeat("0123456789abcdef", 4096)

	srv := startServer(t, Options{Password: testPassword, Handler: func(sess *Session) int {
		if _, err := io.Copy(sess.Stdout, sess.Stdin); err != nil {
			_, _ = io.WriteString(sess.Stderr, err.Error())
			return 1
		}
		return 0
	}})

	client := dial(t, srv, clientConfig(testPassword))
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	var stdout bytes.Buffer
	sess.Stdout = &stdout
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	if err := sess.Start("cat"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := io.WriteString(stdin, payload); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	if err := sess.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if stdout.String() != payload {
		t.Fatalf("echoed %d bytes, want %d", stdout.Len(), len(payload))
	}
}

func TestServerShellHandler(t *testing.T) {
	srv := startServer(t, Options{Password: testPassword, Handler: ShellHandler()})
	client := dial(t, srv, clientConfig(testPassword))

	stdout, stderr, err := runCommand(client, "echo out; echo err >&2; exit 7")
	if stdout != "out\n" {
		t.Errorf("stdout = %q, want %q", stdout, "out\n")
	}
	if stderr != "err\n" {
		t.Errorf("stderr = %q, want %q", stderr, "err\n")
	}
	if code := exitCode(t, err); code != 7 {
		t.Errorf("exit status = %d, want 7", code)
	}
}

func TestServerPty(t *testing.T) {
	requested := make(chan *Pty, 1)
	srv := startServer(t, Options{Password: testPassword, Handler: func(sess *Session) int {
		requested <- sess.Pty
		return 0
	}})

	client := dial(t, srv, clientConfig(testPassword))
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	if err := sess.RequestPty("xterm-256color", 40, 120, nil); err != nil {
		t.Fatalf("RequestPty: %v", err)
	}
	if err := sess.Run("exit 0"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	pty := <-requested
	if pty == nil {
		t.Fatal("handler saw no PTY request")
	}
	if pty.Term != "xterm-256color" || pty.Rows != 40 || pty.Columns != 120 {
		t.Fatalf("pty = %+v", pty)
	}
}

func TestServerWithoutPty(t *testing.T) {
	requested := make(chan *Pty, 1)
	srv := startServer(t, Options{Password: testPassword, Handler: func(sess *Session) int {
		requested <- sess.Pty
		return 0
	}})

	client := dial(t, srv, clientConfig(testPassword))
	if _, _, err := runCommand(client, "exit 0"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if pty := <-requested; pty != nil {
		t.Fatalf("handler saw a PTY request: %+v", pty)
	}
}

func TestTarHandler(t *testing.T) {
	dir := t.TempDir()
	srv := startServer(t, Options{Password: testPassword, Handler: TarHandler(dir)})

	longName := strings.Repeat("long-path-segment/", 8) + "file.txt"
	stream := newTarStream(t,
		tarEntry{name: "plain.txt", body: "plain", mode: 0o640},
		tarEntry{name: "nested/deep/file.txt", body: "nested", mode: 0o600},
		tarEntry{name: "empty", dir: true, mode: 0o750},
		tarEntry{name: "link.txt", link: "plain.txt"},
		tarEntry{name: longName, body: "long", mode: 0o644},
	)

	client := dial(t, srv, clientConfig(testPassword))
	stdout, stderr, err := upload(t, client, stream)
	if err != nil {
		t.Fatalf("upload failed: %v (stdout %q, stderr %q)", err, stdout, stderr)
	}

	got := readFile(t, filepath.Join(dir, "plain.txt"))
	if got != "plain" {
		t.Errorf("plain.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "nested/deep/file.txt")); got != "nested" {
		t.Errorf("nested file = %q", got)
	}
	if info, err := os.Stat(filepath.Join(dir, "empty")); err != nil || !info.IsDir() {
		t.Errorf("empty dir: %v %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "link.txt")); err != nil || target != "plain.txt" {
		t.Errorf("link.txt -> %q, %v", target, err)
	}
	if got := readFile(t, filepath.Join(dir, filepath.FromSlash(longName))); got != "long" {
		t.Errorf("long name file = %q", got)
	}
	info, err := os.Stat(filepath.Join(dir, "plain.txt"))
	if err != nil {
		t.Fatalf("stat plain.txt: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o640 {
		t.Errorf("plain.txt mode = %o, want 640", mode)
	}
}

func TestTarHandlerMalformed(t *testing.T) {
	dir := t.TempDir()
	srv := startServer(t, Options{Password: testPassword, Handler: TarHandler(dir)})

	client := dial(t, srv, clientConfig(testPassword))
	_, stderr, err := upload(t, client, strings.NewReader("not a tar stream"))
	if code := exitCode(t, err); code != 1 {
		t.Fatalf("exit status = %d, want 1", code)
	}
	if !strings.Contains(stderr, "tar:") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestTarHandlerRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	srv := startServer(t, Options{Password: testPassword, Handler: TarHandler(dir)})

	stream := newTarStream(t, tarEntry{name: "../escape.txt", body: "escaped", mode: 0o644})
	client := dial(t, srv, clientConfig(testPassword))
	_, stderr, err := upload(t, client, stream)
	if code := exitCode(t, err); code != 1 {
		t.Fatalf("exit status = %d, want 1", code)
	}
	if !strings.Contains(stderr, "escapes the extraction directory") {
		t.Fatalf("stderr = %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); err == nil {
		t.Fatal("entry escaped the extraction directory")
	}
}

type tarEntry struct {
	name string
	body string
	link string
	dir  bool
	mode int64
}

func newTarStream(t *testing.T, entries ...tarEntry) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for _, entry := range entries {
		hdr := &tar.Header{Name: entry.name, Mode: entry.mode, Format: tar.FormatGNU}
		switch {
		case entry.dir:
			hdr.Typeflag = tar.TypeDir
		case entry.link != "":
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = entry.link
		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(entry.body))
		}
		if err := writer.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %q: %v", entry.name, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.WriteString(writer, entry.body); err != nil {
				t.Fatalf("write tar body %q: %v", entry.name, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	return &buf
}

func upload(t *testing.T, client *ssh.Client, stream io.Reader) (string, string, error) {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	if err := sess.Start("tar -xf -"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := io.Copy(stdin, stream); err != nil {
		t.Fatalf("write tar: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	err = sess.Wait()
	return stdout.String(), stderr.String(), err
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestServerDirectTCPIP(t *testing.T) {
	target := startServer(t, Options{Password: testPassword, Handler: ShellHandler()})
	jump := startServer(t, Options{Password: testPassword, Handler: ShellHandler()})

	client := dial(t, jump, clientConfig(testPassword))
	tunnel, err := client.Dial("tcp", target.Addr())
	if err != nil {
		t.Fatalf("dial through the jump host: %v", err)
	}
	defer func() { _ = tunnel.Close() }()

	conn, chans, reqs, err := ssh.NewClientConn(tunnel, target.Addr(), &ssh.ClientConfig{
		User:            testUser,
		Auth:            []ssh.AuthMethod{ssh.Password(testPassword)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         testTimeout,
	})
	if err != nil {
		t.Fatalf("handshake through the tunnel: %v", err)
	}
	tunnelClient := ssh.NewClient(conn, chans, reqs)
	defer func() { _ = tunnelClient.Close() }()

	stdout, _, err := runCommand(tunnelClient, "echo tunneled")
	if err != nil {
		t.Fatalf("command through the tunnel: %v", err)
	}
	if stdout != "tunneled\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "tunneled\n")
	}
}

func TestServerDirectTCPIPRefused(t *testing.T) {
	jump := startServer(t, Options{Password: testPassword, Handler: ShellHandler()})

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := closed.Addr().String()
	if err := closed.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	client := dial(t, jump, clientConfig(testPassword))
	if _, err := client.Dial("tcp", addr); err == nil {
		t.Fatalf("dial to the closed address %s succeeded", addr)
	}
}

func TestBarrierReleasesEveryHost(t *testing.T) {
	barrier := NewBarrier(2)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- barrier.Wait(testTimeout)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	}
}

func TestBarrierAcceptsExtraArrivals(t *testing.T) {
	barrier := NewBarrier(2)
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- barrier.Wait(testTimeout)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Wait: %v", err)
		}
	}
}

func TestBarrierTimeout(t *testing.T) {
	barrier := NewBarrier(2)
	if err := barrier.Wait(20 * time.Millisecond); err == nil {
		t.Fatal("Wait returned nil with one of two hosts")
	} else if !strings.Contains(err.Error(), "1 of 2 hosts arrived") {
		t.Fatalf("Wait error = %v", err)
	}
}

func TestServerBarrierBothHosts(t *testing.T) {
	barrier := NewBarrier(2)
	handler := func(sess *Session) int {
		if err := barrier.Wait(testTimeout); err != nil {
			_, _ = io.WriteString(sess.Stderr, err.Error())
			return 1
		}
		_, _ = io.WriteString(sess.Stdout, "released\n")
		return 0
	}
	servers := []*Server{
		startServer(t, Options{Password: testPassword, Handler: handler}),
		startServer(t, Options{Password: testPassword, Handler: handler}),
	}

	stdouts := make([]string, len(servers))
	errs := make([]error, len(servers))
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, err := ssh.Dial("tcp", srv.Addr(), clientConfig(testPassword))
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = client.Close() }()
			stdouts[i], _, errs[i] = runCommand(client, "gate")
		}()
	}
	wg.Wait()

	for i := range servers {
		if errs[i] != nil {
			t.Fatalf("host %d: %v", i, errs[i])
		}
		if stdouts[i] != "released\n" {
			t.Errorf("host %d stdout = %q", i, stdouts[i])
		}
	}
}

func TestServerClose(t *testing.T) {
	srv := startServer(t, Options{Password: testPassword, Handler: ShellHandler()})
	addr := srv.Addr()
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		t.Fatal("dial to a closed server succeeded")
	}
}

func TestServerCloseDropsOpenConnections(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := startServer(t, Options{Password: testPassword, Handler: func(*Session) int {
		started <- struct{}{}
		<-release
		return 0
	}})
	client := dial(t, srv, clientConfig(testPassword))

	done := make(chan error, 1)
	go func() {
		_, _, err := runCommand(client, "hang")
		done <- err
	}()
	<-started
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(release)

	select {
	case err := <-done:
		if err == nil {
			t.Error("a command on a closed connection succeeded")
		}
	case <-time.After(testTimeout):
		t.Fatal("Close left the open connection running")
	}
}

func TestNewServerRequiresHandler(t *testing.T) {
	if _, err := NewServer(Options{Password: testPassword}); err == nil {
		t.Fatal("NewServer accepted a nil handler")
	}
}

func TestNewServerRequiresAuth(t *testing.T) {
	if _, err := NewServer(Options{Handler: ShellHandler()}); err == nil {
		t.Fatal("NewServer accepted an empty password and no keys")
	}
}
