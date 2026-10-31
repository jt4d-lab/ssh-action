package sshx

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jt4d-lab/ssh-action/internal/config"
	"github.com/jt4d-lab/ssh-action/internal/sshtest"
)

func writeFile(t *testing.T, name, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(name, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatalf("chmod %s: %v", name, err)
	}
}

// localTree builds a directory with a nested file, an executable and a symlink
// and returns its path.
func localTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app", "config.yml"), "port: 8080\n", 0o640)
	writeFile(t, filepath.Join(dir, "app", "bin", "run.sh"), "#!/bin/sh\n", 0o755)
	if err := os.Symlink("config.yml", filepath.Join(dir, "app", "current.yml")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return dir
}

func TestMatchFiles(t *testing.T) {
	dir := localTree(t)
	writeFile(t, filepath.Join(dir, "readme.txt"), "readme\n", 0o644)
	writeFile(t, filepath.Join(dir, "dist", "one.gz"), "one", 0o644)
	writeFile(t, filepath.Join(dir, "dist", "two.gz"), "two", 0o644)
	writeFile(t, filepath.Join(dir, "other", "one.gz"), "replacement", 0o644)

	tests := []struct {
		name  string
		files string
		want  []string
	}{
		{
			name:  "single file",
			files: filepath.Join(dir, "readme.txt"),
			want:  []string{filepath.Join(dir, "readme.txt")},
		},
		{
			name:  "directory is one entry",
			files: filepath.Join(dir, "app"),
			want:  []string{filepath.Join(dir, "app")},
		},
		{
			name:  "glob",
			files: filepath.Join(dir, "dist", "*.gz"),
			want:  []string{filepath.Join(dir, "dist", "one.gz"), filepath.Join(dir, "dist", "two.gz")},
		},
		{
			name:  "several lines with blanks and spaces",
			files: "\n  " + filepath.Join(dir, "readme.txt") + "  \n\n\t" + filepath.Join(dir, "app") + "\n",
			want:  []string{filepath.Join(dir, "readme.txt"), filepath.Join(dir, "app")},
		},
		{
			name:  "same basename keeps the last match",
			files: filepath.Join(dir, "dist", "one.gz") + "\n" + filepath.Join(dir, "other", "*"),
			want:  []string{filepath.Join(dir, "other", "one.gz")},
		},
		{
			name:  "empty list",
			files: " \n\n",
			want:  nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := MatchFiles(test.files, nil)
			if err != nil {
				t.Fatalf("MatchFiles: %v", err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("matches = %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Errorf("match %d = %q, want %q", i, got[i], test.want[i])
				}
			}
		})
	}
}

// TestMatchFilesDoubleStar pins the documented meaning of "**": filepath.Glob
// has no recursive wildcard, so "**" matches exactly one path element.
func TestMatchFilesDoubleStar(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "dist", "one.gz"), "one", 0o644)
	writeFile(t, filepath.Join(dir, "dist", "nested", "two.gz"), "two", 0o644)

	tests := []struct {
		files string
		want  []string
	}{
		{
			files: filepath.Join(dir, "dist", "**"),
			want:  []string{filepath.Join(dir, "dist", "nested"), filepath.Join(dir, "dist", "one.gz")},
		},
		{
			files: filepath.Join(dir, "dist", "**", "*.gz"),
			want:  []string{filepath.Join(dir, "dist", "nested", "two.gz")},
		},
	}
	for _, test := range tests {
		got, err := MatchFiles(test.files, nil)
		if err != nil {
			t.Fatalf("MatchFiles(%q): %v", test.files, err)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("MatchFiles(%q) = %v, want %v", test.files, got, test.want)
		}
	}
}

func TestMatchFilesInvalid(t *testing.T) {
	dir := t.TempDir()

	for _, files := range []string{
		filepath.Join(dir, "missing.txt"),
		filepath.Join(dir, "*.missing"),
		filepath.Join(dir, "dist", "*.gz"),
		filepath.Join(dir, "[unclosed"),
	} {
		if _, err := MatchFiles(files, nil); err == nil {
			t.Fatalf("MatchFiles(%q) succeeded", files)
		} else if !errors.Is(err, config.ErrInvalidConfig) {
			t.Errorf("MatchFiles(%q) error = %v, want a config error", files, err)
		}
	}
}

func TestMatchFilesWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "dist", "app.gz"), "payload", 0o644)

	lookup := func(name string) (string, bool) {
		if name == workspaceEnv {
			return dir, true
		}
		return "", false
	}
	got, err := MatchFiles("dist/*", lookup)
	if err != nil {
		t.Fatalf("MatchFiles: %v", err)
	}
	if want := filepath.Join(dir, "dist", "app.gz"); len(got) != 1 || got[0] != want {
		t.Fatalf("matches = %v, want [%s]", got, want)
	}

	// Without the workspace a relative pattern resolves against the working
	// directory of the process.
	t.Chdir(dir)
	got, err = MatchFiles("dist/*", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("MatchFiles without a workspace: %v", err)
	}
	if want := filepath.Join("dist", "app.gz"); len(got) != 1 || got[0] != want {
		t.Fatalf("matches = %v, want [%s]", got, want)
	}
}

type tarEntryContent struct {
	header *tar.Header
	body   string
}

func readTarEntries(t *testing.T, stream io.Reader) map[string]tarEntryContent {
	t.Helper()
	entries := make(map[string]tarEntryContent)
	reader := tar.NewReader(stream)
	for {
		hdr, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read %q: %v", hdr.Name, err)
		}
		entries[hdr.Name] = tarEntryContent{header: hdr, body: string(body)}
	}
}

func TestWriteTar(t *testing.T) {
	dir := localTree(t)
	if err := os.Symlink(filepath.Join(dir, "app"), filepath.Join(dir, "app-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var stream bytes.Buffer
	var warn bytes.Buffer
	entries := []string{filepath.Join(dir, "app"), filepath.Join(dir, "app-link")}
	if err := writeTar(&stream, entries, &warn); err != nil {
		t.Fatalf("writeTar: %v", err)
	}
	if warn.Len() != 0 {
		t.Errorf("warnings = %q, want none", warn.String())
	}

	got := readTarEntries(t, &stream)
	checks := []struct {
		name  string
		body  string
		mode  int64
		typ   byte
		link  string
		isDir bool
	}{{name: "app/", typ: tar.TypeDir, mode: 0o755, isDir: true},
		{name: "app/config.yml", typ: tar.TypeReg, body: "port: 8080\n", mode: 0o640},
		{name: "app/bin/", typ: tar.TypeDir, mode: 0o755, isDir: true},
		{name: "app/bin/run.sh", typ: tar.TypeReg, body: "#!/bin/sh\n", mode: 0o755},
		{name: "app/current.yml", typ: tar.TypeSymlink, link: "config.yml"},
		// The top level symlink is dereferenced, so both trees arrive.
		{name: "app-link/", typ: tar.TypeDir, mode: 0o755, isDir: true},
		{name: "app-link/config.yml", typ: tar.TypeReg, body: "port: 8080\n", mode: 0o640},
	}
	for _, check := range checks {
		entry, ok := got[check.name]
		if !ok {
			t.Errorf("entry %q is missing", check.name)
			continue
		}
		if entry.header.Typeflag != check.typ {
			t.Errorf("%q type = %q, want %q", check.name, entry.header.Typeflag, check.typ)
		}
		if check.isDir && !entry.header.FileInfo().IsDir() {
			t.Errorf("%q is not a directory", check.name)
		}
		if check.typ == tar.TypeReg {
			if mode := entry.header.FileInfo().Mode().Perm(); mode != os.FileMode(check.mode) {
				t.Errorf("%q mode = %o, want %o", check.name, mode, check.mode)
			}
		}
		if entry.body != check.body {
			t.Errorf("%q body = %q, want %q", check.name, entry.body, check.body)
		}
		if entry.header.Linkname != check.link {
			t.Errorf("%q link = %q, want %q", check.name, entry.header.Linkname, check.link)
		}
	}
	for name, entry := range got {
		if entry.header.Format != tar.FormatGNU {
			t.Errorf("%q format = %v, want GNU", name, entry.header.Format)
		}
	}
}

func TestWriteTarSkipsUnsupported(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	writeFile(t, filepath.Join(dir, "plain.txt"), "plain", 0o644)

	var stream bytes.Buffer
	var warn bytes.Buffer
	if err := writeTar(&stream, []string{fifo, filepath.Join(dir, "plain.txt")}, &warn); err != nil {
		t.Fatalf("writeTar: %v", err)
	}

	got := readTarEntries(t, &stream)
	if _, ok := got["pipe"]; ok {
		t.Error("the fifo was written to the archive")
	}
	if _, ok := got["plain.txt"]; !ok {
		t.Error("plain.txt is missing from the archive")
	}
	if !strings.Contains(warn.String(), "skip "+fifo) {
		t.Errorf("warnings = %q, want a skipped fifo", warn.String())
	}
}

func TestWriteTarMissingFile(t *testing.T) {
	var stream bytes.Buffer
	err := writeTar(&stream, []string{filepath.Join(t.TempDir(), "gone")}, io.Discard)
	if err == nil {
		t.Fatal("writeTar of a missing file succeeded")
	}
	if errors.Is(err, config.ErrInvalidConfig) {
		t.Errorf("error %v is a config error, want a read error", err)
	}
}

// sessionRecord is what a handler saw of one session.
type sessionRecord struct {
	command string
	pty     *sshtest.Pty
}

// recorder collects the sessions the test server served.
type recorder struct {
	mu       sync.Mutex
	sessions []sessionRecord
}

func (r *recorder) record(sess *sshtest.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = append(r.sessions, sessionRecord{command: sess.Command, pty: sess.Pty})
}

func (r *recorder) all() []sessionRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sessionRecord(nil), r.sessions...)
}

// uploadCommandPrefix starts the command an upload session runs.
const uploadCommandPrefix = "mkdir -p "

// treeHandler serves upload sessions from dir and script sessions with sh.
func treeHandler(dir string, rec *recorder) sshtest.Handler {
	tarHandler := sshtest.TarHandler(dir)
	shellHandler := sshtest.ShellHandler()
	return func(sess *sshtest.Session) int {
		rec.record(sess)
		if strings.HasPrefix(sess.Command, uploadCommandPrefix) {
			return tarHandler(sess)
		}
		return shellHandler(sess)
	}
}

func TestUploadExtractsEntries(t *testing.T) {
	local := localTree(t)
	hostDir := t.TempDir()
	rec := &recorder{}
	srv := startServer(t, treeHandler(hostDir, rec))
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	entries, err := MatchFiles(filepath.Join(local, "app"), nil)
	if err != nil {
		t.Fatalf("MatchFiles: %v", err)
	}

	var stderr, warn bytes.Buffer
	if err := upload(client, host, "/srv/app dir", entries, time.Now().Add(testDeadline), &stderr, &warn); err != nil {
		t.Fatalf("upload: %v (stderr %q)", err, stderr.String())
	}

	if got, want := readFile(t, filepath.Join(hostDir, "app", "config.yml")), "port: 8080\n"; got != want {
		t.Errorf("config.yml = %q, want %q", got, want)
	}
	info, err := os.Stat(filepath.Join(hostDir, "app", "bin", "run.sh"))
	if err != nil {
		t.Fatalf("stat run.sh: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o755 {
		t.Errorf("run.sh mode = %o, want 755", mode)
	}
	if target, err := os.Readlink(filepath.Join(hostDir, "app", "current.yml")); err != nil || target != "config.yml" {
		t.Errorf("current.yml -> %q, %v", target, err)
	}
	if warn.Len() != 0 {
		t.Errorf("warnings = %q, want none", warn.String())
	}

	sessions := rec.all()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %v, want one upload", sessions)
	}
	want := "mkdir -p '/srv/app dir' && tar -C '/srv/app dir' -xf -"
	if sessions[0].command != want {
		t.Errorf("upload command = %q, want %q", sessions[0].command, want)
	}
	if sessions[0].pty != nil {
		t.Errorf("upload session got a pty: %+v", sessions[0].pty)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestUploadLongPath(t *testing.T) {
	local := t.TempDir()
	deep := strings.Repeat("long-segment/", 9) + "file.txt"
	writeFile(t, filepath.Join(local, "app", filepath.FromSlash(deep)), "long\n", 0o644)

	hostDir := t.TempDir()
	srv := startServer(t, sshtest.TarHandler(hostDir))
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	entries, err := MatchFiles(filepath.Join(local, "app"), nil)
	if err != nil {
		t.Fatalf("MatchFiles: %v", err)
	}
	if err := upload(client, host, "/srv/app", entries, time.Now().Add(testDeadline), io.Discard, io.Discard); err != nil {
		t.Fatalf("upload: %v", err)
	}

	if got, want := readFile(t, filepath.Join(hostDir, "app", filepath.FromSlash(deep))), "long\n"; got != want {
		t.Errorf("long path file = %q, want %q", got, want)
	}
}

func TestUploadLargeFile(t *testing.T) {
	local := t.TempDir()
	payload := bytes.Repeat([]byte("ssh-action-large-file-payload\n"), 1<<18)
	writeFile(t, filepath.Join(local, "release", "artifact.bin"), string(payload), 0o644)

	hostDir := t.TempDir()
	srv := startServer(t, sshtest.TarHandler(hostDir))
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	entries, err := MatchFiles(filepath.Join(local, "release"), nil)
	if err != nil {
		t.Fatalf("MatchFiles: %v", err)
	}
	if err := upload(client, host, "/srv/release", entries, time.Now().Add(testDeadline), io.Discard, io.Discard); err != nil {
		t.Fatalf("upload: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(hostDir, "release", "artifact.bin"))
	if err != nil {
		t.Fatalf("read artifact.bin: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("artifact.bin is %d bytes, want %d", len(got), len(payload))
	}
	if !bytes.Equal(got, payload) {
		t.Error("artifact.bin differs from the local payload")
	}
}

func TestUploadTarFailure(t *testing.T) {
	local := localTree(t)
	rec := &recorder{}
	srv := startServer(t, func(sess *sshtest.Session) int {
		rec.record(sess)
		_, _ = io.WriteString(sess.Stderr, "tar: unexpected EOF\n")
		return 1
	})
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	entries, err := MatchFiles(filepath.Join(local, "app"), nil)
	if err != nil {
		t.Fatalf("MatchFiles: %v", err)
	}

	var stderr bytes.Buffer
	err = upload(client, host, "/srv/app", entries, time.Now().Add(testDeadline), &stderr, io.Discard)
	if err == nil {
		t.Fatal("upload with a failing tar succeeded")
	}
	for _, want := range []string{hostName(host), "/srv/app", "status 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if !strings.Contains(stderr.String(), "tar: unexpected EOF") {
		t.Errorf("stderr = %q, want the remote tar message", stderr.String())
	}
	if sessions := rec.all(); len(sessions) != 1 {
		t.Errorf("sessions = %v, want only the failed upload", sessions)
	}
}

// TestUploadWritesOneStderrSafely reports a skipped local entry and the remote
// tar stderr to the same bytes.Buffer, which only survives the two producers
// because upload serializes their writes.
func TestUploadWritesOneStderrSafely(t *testing.T) {
	local := t.TempDir()
	writeFile(t, filepath.Join(local, "app", "payload.bin"), strings.Repeat("x", 1<<20), 0o600)
	if err := syscall.Mkfifo(filepath.Join(local, "app", "0pipe"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	entries, err := MatchFiles(filepath.Join(local, "app"), nil)
	if err != nil {
		t.Fatalf("MatchFiles: %v", err)
	}

	srv := startServer(t, func(sess *sshtest.Session) int {
		for range 100 {
			_, _ = io.WriteString(sess.Stderr, "tar: noise\n")
			time.Sleep(time.Millisecond)
		}
		_, _ = io.Copy(io.Discard, sess.Stdin)
		return 0
	})
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	var stderr bytes.Buffer
	if err := upload(client, host, "/srv/app", entries, time.Now().Add(testDeadline), &stderr, &stderr); err != nil {
		t.Fatalf("upload: %v", err)
	}
	for _, want := range []string{"tar: noise", "warning: skip"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
		}
	}
}

func TestUploadTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := startServer(t, func(sess *sshtest.Session) int {
		<-release
		return 0
	})
	t.Cleanup(func() { close(release) })
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	entries, err := MatchFiles(filepath.Join(localTree(t), "app"), nil)
	if err != nil {
		t.Fatalf("MatchFiles: %v", err)
	}

	start := time.Now()
	err = upload(client, host, "/srv/app", entries, time.Now().Add(100*time.Millisecond), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("upload past the deadline succeeded")
	}
	if !strings.Contains(err.Error(), "upload timeout") {
		t.Errorf("error = %v, want an upload timeout", err)
	}
	if elapsed := time.Since(start); elapsed > testDeadline {
		t.Errorf("timeout took %s", elapsed)
	}
	if _, err := client.NewSession(); err == nil {
		t.Error("client is still open after the upload timeout")
	}

	t.Run("expired deadline", func(t *testing.T) {
		err := upload(client, host, "/srv/app", entries, time.Now().Add(-time.Second), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "upload timeout") {
			t.Errorf("error = %v, want an upload timeout", err)
		}
	})
}

func TestUploadWithRequestPty(t *testing.T) {
	local := localTree(t)
	hostDir := t.TempDir()
	rec := &recorder{}
	srv := startServer(t, treeHandler(hostDir, rec))
	cfg, host := testTarget(t, srv.Addr(), testPassword)
	client := connectTo(t, cfg, host)

	entries, err := MatchFiles(filepath.Join(local, "app"), nil)
	if err != nil {
		t.Fatalf("MatchFiles: %v", err)
	}
	if err := upload(client, host, "/srv/app", entries, time.Now().Add(testDeadline), io.Discard, io.Discard); err != nil {
		t.Fatalf("upload: %v", err)
	}

	var stdout bytes.Buffer
	if err := execScript(client, host, "echo script ran", true, time.Now().Add(testDeadline), &stdout, io.Discard); err != nil {
		t.Fatalf("execScript: %v", err)
	}
	if got, want := stdout.String(), "script ran\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got, want := readFile(t, filepath.Join(hostDir, "app", "config.yml")), "port: 8080\n"; got != want {
		t.Errorf("config.yml = %q, want %q", got, want)
	}

	sessions := rec.all()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %v, want an upload and a script", sessions)
	}
	if sessions[0].pty != nil {
		t.Errorf("upload session got a pty: %+v", sessions[0].pty)
	}
	if sessions[1].pty == nil {
		t.Error("script session got no pty with request_pty")
	}
}
