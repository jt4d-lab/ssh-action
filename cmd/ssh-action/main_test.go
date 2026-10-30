package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jt4d-lab/ssh-action/internal/sshtest"
)

const testPassword = "s3cret"

func startServer(t *testing.T, handler sshtest.Handler) *sshtest.Server {
	t.Helper()
	srv, err := sshtest.NewServer(sshtest.Options{Password: testPassword, Handler: handler})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// inputs returns the lookup of a password-authenticated run against srv with
// extra entries, env values like SECRET included, merged into it.
func inputs(srv *sshtest.Server, extra map[string]string) func(string) (string, bool) {
	vars := map[string]string{
		"INPUT_HOST":            srv.Addr(),
		"INPUT_USERNAME":        "tester",
		"INPUT_PASSWORD":        testPassword,
		"INPUT_TIMEOUT":         "5s",
		"INPUT_COMMAND_TIMEOUT": "10s",
		"INPUT_SCRIPT":          "test -f /dev/null",
	}
	for name, value := range extra {
		vars[name] = value
	}
	return func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}
}

func TestRunSuccess(t *testing.T) {
	srv := startServer(t, func(sess *sshtest.Session) int {
		_, _ = io.WriteString(sess.Stdout, "hello\n")
		return 0
	})

	var stdout, stderr bytes.Buffer
	code := run(nil, inputs(srv, nil), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello\n") {
		t.Errorf("stdout = %q, want the remote output", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Successfully executed commands to all hosts") {
		t.Errorf("stderr = %q, want the success summary", stderr.String())
	}
	if strings.Contains(stdout.String(), "Successfully") {
		t.Errorf("stdout = %q, want service messages kept off stdout", stdout.String())
	}
}

func TestRunCommandFailure(t *testing.T) {
	srv := startServer(t, func(*sshtest.Session) int { return 3 })

	var stdout, stderr bytes.Buffer
	code := run(nil, inputs(srv, nil), &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitFailure, stderr.String())
	}
	for _, want := range []string{srv.Addr(), "status 3"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr %q does not contain %q", stderr.String(), want)
		}
	}
	if strings.Contains(stderr.String(), "Successfully") {
		t.Errorf("stderr = %q, want no success summary after a failure", stderr.String())
	}
}

func TestRunInvalidConfig(t *testing.T) {
	srv := startServer(t, func(*sshtest.Session) int { return 0 })
	lookup := inputs(srv, nil)

	var stdout, stderr bytes.Buffer
	code := run(nil, func(name string) (string, bool) {
		if name != "INPUT_PASSWORD" {
			return lookup(name)
		}
		return "", false
	}, &stdout, &stderr)
	if code != exitConfig {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitConfig, stderr.String())
	}
	if !strings.Contains(stderr.String(), "missing password") {
		t.Errorf("stderr = %q, want the validation error", stderr.String())
	}
}

func TestRunUnmatchedFilesIsConfigError(t *testing.T) {
	srv := startServer(t, func(*sshtest.Session) int { return 0 })

	var stdout, stderr bytes.Buffer
	code := run(nil, inputs(srv, map[string]string{
		"INPUT_WORKDIR": "/srv/app",
		"INPUT_FILES":   "no-such-artifact-*.tar",
	}), &stdout, &stderr)
	if code != exitConfig {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitConfig, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no match") {
		t.Errorf("stderr = %q, want the unmatched pattern error", stderr.String())
	}
}

func TestRunVersion(t *testing.T) {
	original := version
	version = "v1.2.3"
	t.Cleanup(func() { version = original })

	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, func(string) (string, bool) { return "", false }, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), version) {
		t.Errorf("stdout = %q, want the version %q", stdout.String(), version)
	}
}

func TestRunFlagOverridesInput(t *testing.T) {
	srv := startServer(t, func(sess *sshtest.Session) int {
		_, _ = io.WriteString(sess.Stdout, sess.Command+"\n")
		return 0
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"--script", "echo flagged"}, inputs(srv, map[string]string{
		"INPUT_SCRIPT": "echo from-env",
	}), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "echo flagged") {
		t.Errorf("stdout = %q, want the --script text", stdout.String())
	}
	if strings.Contains(stdout.String(), "echo from-env") {
		t.Errorf("stdout = %q, want INPUT_SCRIPT overridden by --script", stdout.String())
	}
}

func TestRunUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--no-such-flag"}, func(string) (string, bool) { return "", false }, &stdout, &stderr)
	if code != exitConfig {
		t.Fatalf("exit code = %d, want %d", code, exitConfig)
	}
	if !strings.Contains(stderr.String(), "no-such-flag") {
		t.Errorf("stderr = %q, want the flag error", stderr.String())
	}
}

func TestRunDebugHidesSecrets(t *testing.T) {
	const envValue = "super-secret-env-value"
	srv := startServer(t, func(sess *sshtest.Session) int {
		_, _ = io.WriteString(sess.Stdout, "hello\n")
		return 0
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"--debug"}, inputs(srv, map[string]string{
		"INPUT_ENVS": "SECRET",
		"SECRET":     envValue,
	}), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitOK, stderr.String())
	}
	if !strings.Contains(stderr.String(), "password: [REDACTED]") {
		t.Errorf("stderr = %q, want the redacted password", stderr.String())
	}
	if strings.Contains(stderr.String(), testPassword) {
		t.Errorf("stderr = %q, leaks the password", stderr.String())
	}
	if !strings.Contains(stderr.String(), "envs: SECRET") {
		t.Errorf("stderr = %q, want the env names", stderr.String())
	}
	if strings.Contains(stderr.String(), envValue) {
		t.Errorf("stderr = %q, leaks the env value", stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello\n") || strings.Contains(stdout.String(), "password:") {
		t.Errorf("stdout = %q, want the remote output only", stdout.String())
	}
}

func TestRunCapturesRemoteStdoutToFile(t *testing.T) {
	srv := startServer(t, func(sess *sshtest.Session) int {
		_, _ = io.WriteString(sess.Stdout, "captured\n")
		return 0
	})
	path := t.TempDir() + "/stdout"

	var stdout, stderr bytes.Buffer
	code := run(nil, inputs(srv, map[string]string{stdoutFileEnv: path}), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "captured\n") {
		t.Errorf("stdout = %q, want the remote output to stay live", stdout.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != "captured\n" {
		t.Errorf("%s = %q, want the remote output only", path, data)
	}
}
