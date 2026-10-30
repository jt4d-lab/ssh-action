package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/jt4d-lab/ssh-action/internal/sshtest"
)

const (
	testUser     = "tester"
	testPassword = "s3cret"
)

// startServer serves args into a buffer and returns the running server and what
// it advertised.
func startServer(t *testing.T, args ...string) (*sshtest.Server, string, string) {
	t.Helper()
	var out bytes.Buffer
	srv, err := serve(args, &out, io.Discard)
	if err != nil {
		t.Fatalf("serve(%q): %v", args, err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	addr, fingerprint := parseInfo(t, out.String())
	return srv, addr, fingerprint
}

// parseInfo reads the addr= and fingerprint= lines of the stdout of a server.
func parseInfo(t *testing.T, out string) (addr, fingerprint string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("advertised line %q is not name=value", line)
		}
		switch name {
		case "addr":
			addr = value
		case "fingerprint":
			fingerprint = value
		default:
			t.Errorf("unexpected advertised line %q", line)
		}
	}
	if addr == "" || fingerprint == "" {
		t.Fatalf("advertised output %q lacks addr or fingerprint", out)
	}
	return addr, fingerprint
}

// dial opens a client session to addr for the given auth methods.
func dial(t *testing.T, addr string, auth ...ssh.AuthMethod) *ssh.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            testUser,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// keyAuth returns the public key auth method of the private key file at path.
func keyAuth(t *testing.T, path string) ssh.AuthMethod {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read client key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatalf("parse client key: %v", err)
	}
	return ssh.PublicKeys(signer)
}

// runCommand runs command over client and returns the combined output.
func runCommand(t *testing.T, client *ssh.Client, command string) (string, error) {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer func() { _ = session.Close() }()
	out, err := session.CombinedOutput(command)
	return string(out), err
}

func TestServeAdvertisesAddrAndFingerprint(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "client_ed25519")
	srv, addr, fingerprint := startServer(t, "-addr", "127.0.0.1:0", "-password", testPassword, "-client-key", keyPath)

	if addr != srv.Addr() {
		t.Errorf("advertised addr = %q, want %q", addr, srv.Addr())
	}
	if want := ssh.FingerprintSHA256(srv.HostKey()); fingerprint != want {
		t.Errorf("advertised fingerprint = %q, want %q", fingerprint, want)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat client key: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("client key mode = %o, want 600", mode)
	}

	t.Run("password", func(t *testing.T) {
		client := dial(t, addr, ssh.Password(testPassword))
		out, err := runCommand(t, client, "printf 'password ok\\n'")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if out != "password ok\n" {
			t.Errorf("output = %q, want %q", out, "password ok\n")
		}
	})

	t.Run("client key", func(t *testing.T) {
		client := dial(t, addr, keyAuth(t, keyPath))
		out, err := runCommand(t, client, "printf 'key ok\\n'")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if out != "key ok\n" {
			t.Errorf("output = %q, want %q", out, "key ok\n")
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		if _, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
			User:            testUser,
			Auth:            []ssh.AuthMethod{ssh.Password("wrong")},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		}); err == nil {
			t.Fatal("dial with a wrong password succeeded")
		}
	})
}

func TestServeReusesClientKey(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "client_ed25519")

	_, firstAddr, _ := startServer(t, "-password", testPassword, "-client-key", keyPath)
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read client key: %v", err)
	}
	firstKeyAuth := keyAuth(t, keyPath)

	_, secondAddr, _ := startServer(t, "-password", testPassword, "-client-key", keyPath)
	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read client key: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the second server replaced the client key")
	}

	for _, addr := range []string{firstAddr, secondAddr} {
		client := dial(t, addr, firstKeyAuth)
		if _, err := runCommand(t, client, "true"); err != nil {
			t.Errorf("host %s rejects the shared client key: %v", addr, err)
		}
	}
}

func TestJumpServerForwardsAndRefusesSessions(t *testing.T) {
	target, targetAddr, _ := startServer(t, "-password", testPassword)
	jump, jumpAddr, _ := startServer(t, "-password", testPassword, "-jump")

	jumpClient := dial(t, jumpAddr, ssh.Password(testPassword))
	if out, err := runCommand(t, jumpClient, "printf 'must not run\\n'"); err == nil {
		t.Fatalf("exec on the jump host succeeded: %q", out)
	} else if !strings.Contains(out, "a jump host runs no sessions") {
		t.Errorf("jump host output = %q, want the refusal message", out)
	}

	conn, err := jumpClient.Dial("tcp", targetAddr)
	if err != nil {
		t.Fatalf("forward to %s through %s: %v", targetAddr, jump.Addr(), err)
	}
	defer func() { _ = conn.Close() }()

	clientConn, chans, reqs, err := ssh.NewClientConn(conn, targetAddr, &ssh.ClientConfig{
		User:            testUser,
		Auth:            []ssh.AuthMethod{ssh.Password(testPassword)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatalf("handshake with the target through the jump host: %v", err)
	}
	targetClient := ssh.NewClient(clientConn, chans, reqs)
	t.Cleanup(func() { _ = targetClient.Close() })

	if target.Addr() != targetAddr {
		t.Fatalf("target listens on %q, forwarded to %q", target.Addr(), targetAddr)
	}
	out, err := runCommand(t, targetClient, "printf 'through the jump ok\\n'")
	if err != nil {
		t.Fatalf("run on the target through the jump host: %v", err)
	}
	if out != "through the jump ok\n" {
		t.Errorf("output = %q, want %q", out, "through the jump ok\n")
	}
}

func TestServeRejectsUnusableOptions(t *testing.T) {
	t.Run("no credentials", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := serve([]string{"-addr", "127.0.0.1:0"}, &out, io.Discard); err == nil {
			t.Fatal("serve accepted a server without password or client key")
		}
	})

	t.Run("unusable client key path", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "absent", "client_ed25519")
		var out bytes.Buffer
		if _, err := serve([]string{"-client-key", missing}, &out, io.Discard); err == nil {
			t.Fatal("serve accepted a client key path in a missing directory")
		}
	})

	t.Run("listening address in use", func(t *testing.T) {
		busy, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer func() { _ = busy.Close() }()

		var out bytes.Buffer
		if _, err := serve([]string{"-addr", busy.Addr().String(), "-password", testPassword}, &out, io.Discard); err == nil {
			t.Fatal("serve accepted an address that is already in use")
		}
	})
}
