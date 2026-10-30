// Command testserver runs the SSH server of internal/sshtest as a process for
// the e2e workflows. It serves exec requests with the real sh, and therefore the
// real tar, of the machine it runs on, so a copy step of the action unpacks into
// the file system exactly as it does on a real host.
//
// After the listeners are up it writes the lines addr=<host:port> and
// fingerprint=<SHA256 host key fingerprint> to stdout, which the workflow reads
// back to fill the host and fingerprint inputs. The process serves until it is
// interrupted.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/ssh"

	"github.com/jt4d-lab/ssh-action/internal/sshtest"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "testserver: %v\n", err)
		os.Exit(1)
	}
}

// run serves the test server described by args until the process is interrupted.
func run(args []string, stdout, stderr io.Writer) error {
	srv, err := serve(args, stdout, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	return nil
}

// serve starts the server described by args and writes its addr= and
// fingerprint= lines to stdout.
func serve(args []string, stdout, stderr io.Writer) (*sshtest.Server, error) {
	fs := flag.NewFlagSet("testserver", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:0", "listen address")
	password := fs.String("password", "", "password accepted for authentication")
	clientKey := fs.String("client-key", "", "path of the client private key to authorize; the key is generated when the file does not exist")
	jump := fs.Bool("jump", false, "serve as a jump host: forward connections and refuse exec")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	opts := sshtest.Options{
		Addr:     *addr,
		Password: *password,
		Handler:  sshtest.ShellHandler(),
	}
	if *jump {
		opts.Handler = refuseExec
	}
	if *clientKey != "" {
		signer, err := loadOrCreateClientKey(*clientKey)
		if err != nil {
			return nil, err
		}
		opts.AuthorizedKeys = []ssh.PublicKey{signer.PublicKey()}
	}

	srv, err := sshtest.NewServer(opts)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(stdout, "addr=%s\nfingerprint=%s\n", srv.Addr(), ssh.FingerprintSHA256(srv.HostKey())); err != nil {
		_ = srv.Close()
		return nil, err
	}
	return srv, nil
}

// refuseExec is the handler of a jump host, which carries the connections of the
// action without running anything of it.
func refuseExec(sess *sshtest.Session) int {
	_, _ = fmt.Fprintln(sess.Stderr, "testserver: a jump host runs no sessions")
	return 1
}

// loadOrCreateClientKey returns the client key at path as a signer. The key is
// generated when the file is absent, so several servers of one job can share a
// single client key without ordering their startup.
func loadOrCreateClientKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if data, err = createClientKey(path); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, fmt.Errorf("read client key %s: %w", path, err)
	}

	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse client key %s: %w", path, err)
	}
	return signer, nil
}

// createClientKey writes a fresh ed25519 OpenSSH private key to path and returns
// it, because the key input of the action takes the key content. A key that
// another server created first is read instead of overwritten.
func createClientKey(path string) ([]byte, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate client key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, fmt.Errorf("marshal client key: %w", err)
	}
	data := pem.EncodeToMemory(block)

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read client key %s: %w", path, err)
		}
		return data, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create client key %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write client key %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("write client key %s: %w", path, err)
	}
	return data, nil
}
