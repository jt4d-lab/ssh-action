// Package sshtest runs an in-process SSH server for integration tests.
package sshtest

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Pty is the terminal a client requested for a session.
type Pty struct {
	Term    string
	Columns uint32
	Rows    uint32
}

// Session is one accepted SSH session channel.
type Session struct {
	// Command is the command line of the exec request.
	Command string
	// Pty is nil unless the client requested a terminal.
	Pty *Pty
	// Stdin ends at EOF when the client closes its write side.
	Stdin io.Reader
	// Stdout and Stderr are distinct streams even when a terminal was
	// requested, because no terminal is allocated.
	Stdout io.Writer
	Stderr io.Writer
}

// Handler serves one session and returns the exit status sent to the client.
type Handler func(*Session) int

// Options configures a Server.
type Options struct {
	// Addr is the listen address; "127.0.0.1:0" is used when empty.
	Addr string
	// Password, when not empty, is accepted for password authentication.
	Password string
	// AuthorizedKeys are the public keys accepted for public key authentication.
	AuthorizedKeys []ssh.PublicKey
	// Handler serves the exec requests and is required.
	Handler Handler
}

// Server is an SSH server accepting password and public key sessions.
type Server struct {
	ln      net.Listener
	config  *ssh.ServerConfig
	handler Handler
	hostKey ssh.Signer

	mu     sync.Mutex
	conns  map[*ssh.ServerConn]struct{}
	closed bool
}

// NewServer starts a server on Options.Addr and returns it listening.
func NewServer(opts Options) (*Server, error) {
	if opts.Handler == nil {
		return nil, errors.New("sshtest: missing handler")
	}
	if opts.Password == "" && len(opts.AuthorizedKeys) == 0 {
		return nil, errors.New("sshtest: missing password or authorized keys")
	}

	hostKey, err := newHostKey()
	if err != nil {
		return nil, err
	}

	config := &ssh.ServerConfig{}
	if opts.Password != "" {
		password := []byte(opts.Password)
		config.PasswordCallback = func(_ ssh.ConnMetadata, given []byte) (*ssh.Permissions, error) {
			if subtle.ConstantTimeCompare(given, password) != 1 {
				return nil, errAuthFailed
			}
			return nil, nil
		}
	}
	if len(opts.AuthorizedKeys) > 0 {
		authorized := make([][]byte, len(opts.AuthorizedKeys))
		for i, key := range opts.AuthorizedKeys {
			authorized[i] = key.Marshal()
		}
		config.PublicKeyCallback = func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			presented := key.Marshal()
			for _, want := range authorized {
				if bytes.Equal(presented, want) {
					return nil, nil
				}
			}
			return nil, errAuthFailed
		}
	}
	config.AddHostKey(hostKey)

	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("sshtest: listen: %w", err)
	}

	srv := &Server{
		ln:      ln,
		config:  config,
		handler: opts.Handler,
		hostKey: hostKey,
		conns:   make(map[*ssh.ServerConn]struct{}),
	}
	go srv.accept()
	return srv, nil
}

var errAuthFailed = errors.New("sshtest: authentication failed")

// Addr is the address the server listens on, as "host:port".
func (s *Server) Addr() string {
	return s.ln.Addr().String()
}

// HostKey is the public part of the host key a client sees.
func (s *Server) HostKey() ssh.PublicKey {
	return s.hostKey.PublicKey()
}

// newHostKey generates the ed25519 host key every server presents.
func newHostKey() (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("sshtest: generate host key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("sshtest: host key signer: %w", err)
	}
	return signer, nil
}

// Close stops accepting connections and drops every open one.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	conns := make([]*ssh.ServerConn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()

	err := s.ln.Close()
	for _, conn := range conns {
		_ = conn.Close()
	}
	return err
}

func (s *Server) accept() {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(nc)
	}
}

func (s *Server) handleConn(nc net.Conn) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, s.config)
	if err != nil {
		_ = nc.Close()
		return
	}
	if !s.track(conn) {
		_ = conn.Close()
		return
	}
	defer func() {
		s.untrack(conn)
		_ = conn.Close()
	}()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		switch newChan.ChannelType() {
		case "session":
			ch, chReqs, err := newChan.Accept()
			if err != nil {
				continue
			}
			go s.serveSession(ch, chReqs)
		case "direct-tcpip":
			go s.forward(newChan)
		default:
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
	}
}

func (s *Server) track(conn *ssh.ServerConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[conn] = struct{}{}
	return true
}

func (s *Server) untrack(conn *ssh.ServerConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

func (s *Server) serveSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer func() { _ = ch.Close() }()
	sess := &Session{Stdin: ch, Stdout: ch, Stderr: ch.Stderr()}

	for req := range reqs {
		switch req.Type {
		case "pty-req":
			pty, err := parsePty(req.Payload)
			if err != nil {
				_ = req.Reply(false, nil)
				continue
			}
			sess.Pty = pty
			_ = req.Reply(true, nil)
		case "env", "window-change":
			_ = req.Reply(true, nil)
		case "exec":
			command, err := parseCommand(req.Payload)
			if err != nil {
				_ = req.Reply(false, nil)
				return
			}
			sess.Command = command
			_ = req.Reply(true, nil)
			status := s.handler(sess)
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatus{Status: uint32(status)}))
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// forward serves a direct-tcpip channel, which is how an SSH client reaches a
// target through a jump host.
func (s *Server) forward(newChan ssh.NewChannel) {
	var req struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(newChan.ExtraData(), &req); err != nil {
		_ = newChan.Reject(ssh.ConnectionFailed, "malformed direct-tcpip request")
		return
	}

	target, err := net.Dial("tcp", net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port))))
	if err != nil {
		_ = newChan.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	defer func() { _ = target.Close() }()

	ch, chReqs, err := newChan.Accept()
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	go ssh.DiscardRequests(chReqs)

	// Each direction signals once; the spare slot lets the copy that loses the
	// race exit after the forward has returned.
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(ch, target)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(target, ch)
		done <- struct{}{}
	}()
	<-done
}

type exitStatus struct {
	Status uint32
}

func parsePty(payload []byte) (*Pty, error) {
	var req struct {
		Term     string
		Columns  uint32
		Rows     uint32
		Width    uint32
		Height   uint32
		Modelist string
	}
	if err := ssh.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	return &Pty{Term: req.Term, Columns: req.Columns, Rows: req.Rows}, nil
}

func parseCommand(payload []byte) (string, error) {
	var req struct {
		Command string
	}
	if err := ssh.Unmarshal(payload, &req); err != nil {
		return "", err
	}
	return req.Command, nil
}

// ShellHandler returns a Handler that runs Session.Command with sh -c on the
// machine running the tests and reports the exit status of that shell.
func ShellHandler() Handler {
	return func(sess *Session) int {
		cmd := exec.Command("sh", "-c", sess.Command)
		cmd.Stdin = sess.Stdin
		cmd.Stdout = sess.Stdout
		cmd.Stderr = sess.Stderr
		err := cmd.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if code := exitErr.ExitCode(); code >= 0 {
				return code
			}
			return 1
		}
		if err != nil {
			_, _ = fmt.Fprintf(sess.Stderr, "sh: %v\n", err)
			return 1
		}
		return 0
	}
}

// TarHandler returns a Handler that unpacks the tar stream read from
// Session.Stdin into dir and reports the number of extracted entries on stdout.
// A malformed stream is reported on stderr with exit status 1.
func TarHandler(dir string) Handler {
	return func(sess *Session) int {
		extracted, err := extractTar(dir, sess.Stdin)
		if err != nil {
			_, _ = fmt.Fprintf(sess.Stderr, "tar: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(sess.Stdout, "extracted %d entries\n", extracted)
		return 0
	}
}

func extractTar(dir string, stream io.Reader) (int, error) {
	reader := tar.NewReader(stream)
	extracted := 0
	for {
		hdr, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return extracted, nil
		}
		if err != nil {
			return extracted, err
		}
		target, err := extractionTarget(dir, hdr.Name)
		if err != nil {
			return extracted, err
		}
		if err := writeEntry(target, hdr, reader); err != nil {
			return extracted, err
		}
		extracted++
	}
}

func writeEntry(target string, hdr *tar.Header, content io.Reader) error {
	mode := hdr.FileInfo().Mode().Perm()
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(target, mode); err != nil {
			return err
		}
		return os.Chmod(target, mode)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(file, content); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		return os.Chmod(target, mode)
	case tar.TypeSymlink:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.Symlink(hdr.Linkname, target)
	}
	return fmt.Errorf("entry %q: unsupported type %q", hdr.Name, hdr.Typeflag)
}

func extractionTarget(dir, name string) (string, error) {
	target := filepath.Join(dir, filepath.FromSlash(name))
	rel, err := filepath.Rel(dir, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("entry %q escapes the extraction directory", name)
	}
	return target, nil
}

// Barrier releases every expected host at the same time, so a test proves the
// hosts run concurrently: a host that never arrives leaves the others blocked
// until their timeout.
type Barrier struct {
	expect  int
	release chan struct{}

	mu      sync.Mutex
	arrived int
}

// NewBarrier returns a Barrier that opens once expect hosts have called Wait.
func NewBarrier(expect int) *Barrier {
	return &Barrier{expect: expect, release: make(chan struct{})}
}

// Wait blocks until expect hosts have called Wait and returns nil, or returns an
// error when timeout elapses first.
func (b *Barrier) Wait(timeout time.Duration) error {
	b.mu.Lock()
	b.arrived++
	arrived := b.arrived
	if arrived == b.expect {
		close(b.release)
	}
	release := b.release
	b.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-release:
		return nil
	case <-timer.C:
		return fmt.Errorf("barrier: %d of %d hosts arrived within %s", arrived, b.expect, timeout)
	}
}
