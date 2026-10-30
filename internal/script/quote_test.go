package script_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jt4d-lab/ssh-action/internal/config"
	"github.com/jt4d-lab/ssh-action/internal/script"
)

func TestQuotePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "plain path",
			path: "/var/www/app",
			want: `'/var/www/app'`,
		},
		{
			name: "path with space",
			path: "/var/www/my app",
			want: `'/var/www/my app'`,
		},
		{
			name: "path with single quote",
			path: "/var/www/o'brien",
			want: `'/var/www/o'\''brien'`,
		},
		{
			name: "path with double quote and dollar",
			path: `/var/www/"$HOME"/app`,
			want: `'/var/www/"$HOME"/app'`,
		},
		{
			name: "current user home",
			path: "~",
			want: `"$HOME"`,
		},
		{
			name: "path under the current user home",
			path: "~/deploy",
			want: `"$HOME"/'deploy'`,
		},
		{
			name: "nested path under home with space and quote",
			path: "~/releases/a b/o'brien",
			want: `"$HOME"/'releases/a b/o'\''brien'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := script.QuotePath(tt.path)
			if err != nil {
				t.Fatalf("QuotePath(%q): %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("QuotePath(%q) = %s, want %s", tt.path, got, tt.want)
			}
		})
	}
}

func TestQuotePathInvalid(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantMsg string
	}{
		{
			name:    "empty path",
			path:    "",
			wantMsg: "empty path",
		},
		{
			name:    "other user home with subdirectory",
			path:    "~root/deploy",
			wantMsg: "only the current user home",
		},
		{
			name:    "bare other user home",
			path:    "~root",
			wantMsg: "only the current user home",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := script.QuotePath(tt.path)
			if !errors.Is(err, config.ErrInvalidConfig) {
				t.Fatalf("QuotePath(%q) error = %v, want ErrInvalidConfig", tt.path, err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("QuotePath(%q) error = %q, want it to contain %q", tt.path, err, tt.wantMsg)
			}
		})
	}
}

func TestQuotePathInShell(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	home := filepath.Join(root, "home")
	dirs := []string{
		filepath.Join(root, "plain"),
		filepath.Join(root, "with space"),
		filepath.Join(root, "quote'name"),
		filepath.Join(root, "dollar$sign"),
		filepath.Join(home, "sub dir"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "plain path", path: dirs[0], want: dirs[0]},
		{name: "path with space", path: dirs[1], want: dirs[1]},
		{name: "path with single quote", path: dirs[2], want: dirs[2]},
		{name: "path with dollar", path: dirs[3], want: dirs[3]},
		{name: "current user home", path: "~", want: home},
		{name: "path under home with space", path: "~/sub dir", want: dirs[4]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quoted, err := script.QuotePath(tt.path)
			if err != nil {
				t.Fatalf("QuotePath(%q): %v", tt.path, err)
			}
			cmd := exec.Command("sh", "-c", "cd "+quoted+" && pwd")
			cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("sh -c cd %s: %v\n%s", quoted, err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tt.want {
				t.Errorf("cd %s landed in %s, want %s", quoted, got, tt.want)
			}
		})
	}
}
