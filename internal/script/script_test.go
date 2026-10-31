package script_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jt4d-lab/ssh-action/internal/config"
	"github.com/jt4d-lab/ssh-action/internal/script"
)

type fakeEnv struct {
	vars    map[string]string
	environ []string
}

func (e fakeEnv) Lookup(name string) (string, bool) {
	v, ok := e.vars[name]
	return v, ok
}

func (e fakeEnv) Environ() []string { return e.environ }

func TestBuildOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deploy.sh")
	if err := os.WriteFile(path, []byte("from_file\n\nalso_from_file\n"), 0o644); err != nil {
		t.Fatalf("write script_path: %v", err)
	}

	cfg := &config.Config{
		Workdir:    "/srv/app",
		Envs:       "FOO",
		EnvsFormat: config.DefaultEnvsFormat,
		Script:     "first\n\nsecond",
		ScriptPath: path,
	}
	got, err := script.Build(cfg, fakeEnv{vars: map[string]string{"FOO": "x"}}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "cd '/srv/app' || exit 1\n" +
		"export FOO='x'\n" +
		"first\nsecond\nfrom_file\nalso_from_file"
	if got != want {
		t.Errorf("Build() = %q, want %q", got, want)
	}
}

func TestBuildScriptPathUnreadable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sh")
	_, err := script.Build(&config.Config{ScriptPath: missing}, fakeEnv{}, nil)
	if err == nil {
		t.Fatal("Build() error = nil, want an error for a missing script_path")
	}
	if errors.Is(err, config.ErrInvalidConfig) {
		t.Errorf("Build() error = %v, want an execution error, not ErrInvalidConfig", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("Build() error = %q, want it to name %q", err, missing)
	}
}

func TestBuildWorkdirProlog(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{
			name: "create_workdir adds mkdir",
			cfg:  config.Config{Script: "true", Workdir: "/srv/app", CreateWorkdir: true},
			want: "mkdir -p '/srv/app' && cd '/srv/app' || exit 1\ntrue",
		},
		{
			name: "files adds mkdir without create_workdir",
			cfg:  config.Config{Script: "true", Workdir: "/srv/app", Files: "dist/*"},
			want: "mkdir -p '/srv/app' && cd '/srv/app' || exit 1\ntrue",
		},
		{
			name: "no mkdir without create_workdir",
			cfg:  config.Config{Script: "true", Workdir: "/srv/app"},
			want: "cd '/srv/app' || exit 1\ntrue",
		},
		{
			name: "path under home is quoted through QuotePath",
			cfg:  config.Config{Script: "true", Workdir: "~/deploy app", CreateWorkdir: true},
			want: `mkdir -p "$HOME"/'deploy app' && cd "$HOME"/'deploy app' || exit 1` + "\ntrue",
		},
		{
			name: "no workdir no prolog",
			cfg:  config.Config{Script: "true"},
			want: "true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := script.Build(&tt.cfg, fakeEnv{}, nil)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if got != tt.want {
				t.Errorf("Build() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildWorkdirInvalid(t *testing.T) {
	cfg := &config.Config{Script: "true", Workdir: "~root/app", CreateWorkdir: true}
	_, err := script.Build(cfg, fakeEnv{}, nil)
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("Build() error = %v, want ErrInvalidConfig", err)
	}
}

func TestBuildWorkdirPrologInShell(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	cfg := &config.Config{Script: "pwd", Workdir: "~/new app", CreateWorkdir: true}
	text, err := script.Build(cfg, fakeEnv{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	out, code := runShell(t, text, home)
	if code != 0 {
		t.Fatalf("script exited with %d: %q", code, out)
	}
	if want := filepath.Join(home, "new app"); strings.TrimSpace(out) != want {
		t.Errorf("script landed in %q, want %q", strings.TrimSpace(out), want)
	}
}

func TestBuildEnvs(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		env  fakeEnv
		want string
	}{
		{
			name: "names are upper-cased and values shell quoted",
			cfg:  config.Config{Script: "true", Envs: "foo, bar", EnvsFormat: config.DefaultEnvsFormat},
			env:  fakeEnv{vars: map[string]string{"FOO": "hello world", "BAR": "a'b$c"}},
			want: "export FOO='hello world'\nexport BAR='a'\\''b$c'\ntrue",
		},
		{
			name: "custom format",
			cfg:  config.Config{Script: "true", Envs: "FOO", EnvsFormat: "setenv {NAME} {VALUE}"},
			env:  fakeEnv{vars: map[string]string{"FOO": "x y"}},
			want: "setenv FOO 'x y'\ntrue",
		},
		{
			name: "set but empty value is exported",
			cfg:  config.Config{Script: "true", Envs: "EMPTY", EnvsFormat: config.DefaultEnvsFormat},
			env:  fakeEnv{vars: map[string]string{"EMPTY": ""}},
			want: "export EMPTY=''\ntrue",
		},
		{
			name: "empty envs entry is skipped",
			cfg:  config.Config{Script: "true", Envs: " , FOO ,", EnvsFormat: config.DefaultEnvsFormat},
			env:  fakeEnv{vars: map[string]string{"FOO": "x"}},
			want: "export FOO='x'\ntrue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := script.Build(&tt.cfg, tt.env, nil)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if got != tt.want {
				t.Errorf("Build() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildEnvsMissing(t *testing.T) {
	var warn bytes.Buffer
	cfg := &config.Config{Script: "true", Envs: "FOO, MISSING", EnvsFormat: config.DefaultEnvsFormat}
	got, err := script.Build(cfg, fakeEnv{vars: map[string]string{"FOO": "x"}}, &warn)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := "export FOO='x'\ntrue"; got != want {
		t.Errorf("Build() = %q, want %q", got, want)
	}
	if msg := warn.String(); !strings.Contains(msg, "MISSING") {
		t.Errorf("warning = %q, want it to name MISSING", msg)
	}
}

func TestBuildAllEnvs(t *testing.T) {
	env := fakeEnv{environ: []string{
		"PATH=/usr/bin",
		"DRONE_BUILD=1",
		"PLUGIN_TOKEN=s3cret",
		"INPUT_KEY=key",
		"GITHUB_TOKEN=gh",
		"MY_VAR=x",
		"BROKEN",
	}}

	t.Run("prefixed variables only", func(t *testing.T) {
		got, err := script.Build(&config.Config{Script: "true", AllEnvs: true, EnvsFormat: config.DefaultEnvsFormat}, env, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		want := "export DRONE_BUILD='1'\n" +
			"export PLUGIN_TOKEN='s3cret'\n" +
			"export INPUT_KEY='key'\n" +
			"export GITHUB_TOKEN='gh'\n" +
			"true"
		if got != want {
			t.Errorf("Build() = %q, want %q", got, want)
		}
	})

	t.Run("disabled exports nothing", func(t *testing.T) {
		got, err := script.Build(&config.Config{Script: "true"}, env, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got != "true" {
			t.Errorf("Build() = %q, want %q", got, "true")
		}
	})
}

func TestBuildEnvsPrecedence(t *testing.T) {
	env := fakeEnv{
		vars:    map[string]string{"INPUT_FILES": "explicit"},
		environ: []string{"INPUT_FILES=from_environ"},
	}
	got, err := script.Build(&config.Config{Script: "true", Envs: "input_files", AllEnvs: true, EnvsFormat: config.DefaultEnvsFormat}, env, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := "export INPUT_FILES='explicit'\ntrue"; got != want {
		t.Errorf("Build() = %q, want %q", got, want)
	}
}

func TestBuildEnvsInShell(t *testing.T) {
	value := `p@ss w'rd "$HOME" ` + "`uname`" + ` \ end`
	cfg := &config.Config{
		Script:     `printf '%s\n' "$PLUGIN_VALUE"`,
		Envs:       "PLUGIN_VALUE",
		EnvsFormat: config.DefaultEnvsFormat,
	}
	text, err := script.Build(cfg, fakeEnv{vars: map[string]string{"PLUGIN_VALUE": value}}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	out, code := runShell(t, text, t.TempDir())
	if code != 0 {
		t.Fatalf("script exited with %d: %q", code, out)
	}
	if want := value + "\n"; out != want {
		t.Errorf("exported value = %q, want %q", out, want)
	}
}

const testScriptStop = "set -e"

func TestBuildScriptStop(t *testing.T) {
	t.Run("set -e opens the body", func(t *testing.T) {
		cfg := &config.Config{Script: "echo one\n\nfalse\necho two", ScriptStop: true}
		got, err := script.Build(cfg, fakeEnv{}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		want := strings.Join([]string{testScriptStop, "echo one", "false", "echo two"}, "\n")
		if got != want {
			t.Errorf("Build() = %q, want %q", got, want)
		}
	})

	t.Run("no set -e without script_stop", func(t *testing.T) {
		cfg := &config.Config{Script: "echo one\nfalse"}
		got, err := script.Build(cfg, fakeEnv{}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if want := "echo one\nfalse"; got != want {
			t.Errorf("Build() = %q, want %q", got, want)
		}
	})

	t.Run("set -e follows the prolog and the env exports", func(t *testing.T) {
		cfg := &config.Config{
			Workdir:    "/srv/app",
			Envs:       "FOO",
			EnvsFormat: config.DefaultEnvsFormat,
			Script:     "false",
			ScriptStop: true,
		}
		got, err := script.Build(cfg, fakeEnv{vars: map[string]string{"FOO": "x"}}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		want := "cd '/srv/app' || exit 1\nexport FOO='x'\n" + testScriptStop + "\nfalse"
		if got != want {
			t.Errorf("Build() = %q, want %q", got, want)
		}
	})

	t.Run("script_path lines are covered", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "deploy.sh")
		if err := os.WriteFile(path, []byte("false\n"), 0o644); err != nil {
			t.Fatalf("write script_path: %v", err)
		}
		got, err := script.Build(&config.Config{ScriptPath: path, ScriptStop: true}, fakeEnv{}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if want := testScriptStop + "\nfalse"; got != want {
			t.Errorf("Build() = %q, want %q", got, want)
		}
	})

	t.Run("empty body stays empty", func(t *testing.T) {
		got, err := script.Build(&config.Config{Workdir: "/srv/app", ScriptStop: true}, fakeEnv{}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if want := "cd '/srv/app' || exit 1"; got != want {
			t.Errorf("Build() = %q, want %q", got, want)
		}
	})
}

func TestBuildScriptStopInShell(t *testing.T) {
	t.Run("stops after the first failure", func(t *testing.T) {
		cfg := &config.Config{Script: "echo one\nfalse\necho two", ScriptStop: true}
		text, err := script.Build(cfg, fakeEnv{}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		out, code := runShell(t, text, t.TempDir())
		if out != "one\n" {
			t.Errorf("stdout = %q, want %q", out, "one\n")
		}
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	})

	t.Run("keeps the remote exit code", func(t *testing.T) {
		cfg := &config.Config{Script: "exit 7\necho two", ScriptStop: true}
		text, err := script.Build(cfg, fakeEnv{}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		out, code := runShell(t, text, t.TempDir())
		if out != "" {
			t.Errorf("stdout = %q, want empty", out)
		}
		if code != 7 {
			t.Errorf("exit code = %d, want 7", code)
		}
	})

	t.Run("continuation line stays a single command", func(t *testing.T) {
		cfg := &config.Config{Script: "echo one \\\n  two\nfalse\necho three", ScriptStop: true}
		text, err := script.Build(cfg, fakeEnv{}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		out, code := runShell(t, text, t.TempDir())
		if out != "one two\n" {
			t.Errorf("stdout = %q, want %q", out, "one two\n")
		}
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	})
}

func TestBuildScriptStopBlocksInShell(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		wantOut  string
		wantCode int
	}{
		{
			name: "if/else with a false condition",
			script: "if false; then\n" +
				"  printf 'yes\\n'\n" +
				"else\n" +
				"  printf 'no\\n'\n" +
				"fi\n" +
				"printf 'after\\n'",
			wantOut:  "no\nafter\n",
			wantCode: 0,
		},
		{
			name: "case block",
			script: "case two in\n" +
				"  one)\n" +
				"    printf 'one\\n'\n" +
				"    ;;\n" +
				"  two)\n" +
				"    printf 'two\\n'\n" +
				"    ;;\n" +
				"esac\n" +
				"printf 'after\\n'",
			wantOut:  "two\nafter\n",
			wantCode: 0,
		},
		{
			name: "for and while loops",
			script: "for i in 1 2; do\n" +
				"  printf 'for %s\\n' \"$i\"\n" +
				"done\n" +
				"n=0\n" +
				"while [ \"$n\" -lt 1 ]; do\n" +
				"  n=$((n + 1))\n" +
				"  printf 'while %s\\n' \"$n\"\n" +
				"done\n" +
				"printf 'after\\n'",
			wantOut:  "for 1\nfor 2\nwhile 1\nafter\n",
			wantCode: 0,
		},
		{
			name: "brace and subshell groups",
			script: "{\n" +
				"  printf 'brace\\n'\n" +
				"}\n" +
				"(\n" +
				"  printf 'subshell\\n'\n" +
				")\n" +
				"printf 'after\\n'",
			wantOut:  "brace\nsubshell\nafter\n",
			wantCode: 0,
		},
		{
			name: "failure inside an if body stops the script",
			script: "if true; then\n" +
				"  printf 'inside\\n'\n" +
				"  exit 9\n" +
				"fi\n" +
				"printf 'after\\n'",
			wantOut:  "inside\n",
			wantCode: 9,
		},
		{
			name: "failure inside a case item stops the script",
			script: "case one in\n" +
				"  one)\n" +
				"    false\n" +
				"    printf 'not reached\\n'\n" +
				"    ;;\n" +
				"esac\n" +
				"printf 'after\\n'",
			wantOut:  "",
			wantCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, err := script.Build(&config.Config{Script: tt.script, ScriptStop: true}, fakeEnv{}, nil)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			out, code := runShell(t, text, t.TempDir())
			if out != tt.wantOut {
				t.Errorf("stdout = %q, want %q", out, tt.wantOut)
			}
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
		})
	}
}

func runShell(t *testing.T, text, home string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", text)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	code := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("sh -c: %v\n%s", err, stderr.String())
		}
		code = exitErr.ExitCode()
	}
	return stdout.String(), code
}
