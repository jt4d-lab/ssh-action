package script

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jt4d-lab/ssh-action/internal/config"
)

// Env is the process environment that envs and allenvs are read from.
type Env interface {
	Lookup(name string) (string, bool)
	Environ() []string
}

// allEnvsPrefixes lists the variable prefixes forwarded to the host by allenvs.
var allEnvsPrefixes = []string{"DRONE_", "PLUGIN_", "INPUT_", "GITHUB_"}

// scriptStopGuard saves the status of the preceding line and leaves the shell
// on a non-zero one.
const scriptStopGuard = `_ssh_action_rc=$?; [ "$_ssh_action_rc" -eq 0 ] || exit "$_ssh_action_rc"`

// Build assembles the remote shell script from cfg: the workdir prolog, the env
// exports, the lines of cfg.Script and the lines of the file named by
// cfg.ScriptPath, in that order. Blank lines are dropped. cfg.Envs is a
// comma-separated list of variable names, upper-cased and read through env;
// variables that env does not have are reported to warn and skipped.
func Build(cfg *config.Config, env Env, warn io.Writer) (string, error) {
	var lines []string

	if cfg.Workdir != "" {
		prolog, err := workdirProlog(cfg)
		if err != nil {
			return "", err
		}
		lines = append(lines, prolog)
	}

	lines = append(lines, envExports(cfg, env, warn)...)

	body := splitLines(cfg.Script)
	if cfg.ScriptPath != "" {
		data, err := os.ReadFile(cfg.ScriptPath)
		if err != nil {
			return "", fmt.Errorf("read script_path: %w", err)
		}
		body = append(body, splitLines(string(data))...)
	}
	if cfg.ScriptStop {
		body = withScriptStop(body)
	}
	lines = append(lines, body...)

	return strings.Join(lines, "\n"), nil
}

func workdirProlog(cfg *config.Config) (string, error) {
	dir, err := QuotePath(cfg.Workdir)
	if err != nil {
		return "", err
	}
	cd := "cd " + dir + " || exit 1"
	if cfg.CreateWorkdir || strings.TrimSpace(cfg.Files) != "" {
		return "mkdir -p " + dir + " && " + cd, nil
	}
	return cd, nil
}

func envExports(cfg *config.Config, env Env, warn io.Writer) []string {
	format := cfg.EnvsFormat
	if format == "" {
		format = config.DefaultEnvsFormat
	}
	seen := make(map[string]bool)
	var lines []string

	add := func(name, value string) {
		name = strings.ToUpper(name)
		if seen[name] {
			return
		}
		seen[name] = true
		line := strings.ReplaceAll(format, "{NAME}", name)
		lines = append(lines, strings.ReplaceAll(line, "{VALUE}", singleQuote(value)))
	}

	for _, name := range strings.Split(cfg.Envs, ",") {
		if name = strings.ToUpper(strings.TrimSpace(name)); name == "" {
			continue
		}
		value, ok := env.Lookup(name)
		if !ok {
			if warn != nil {
				_, _ = fmt.Fprintf(warn, "warning: env %s is not set, skipped\n", name)
			}
			continue
		}
		add(name, value)
	}

	if cfg.AllEnvs {
		for _, entry := range env.Environ() {
			name, value, ok := strings.Cut(entry, "=")
			if !ok || !hasAllEnvsPrefix(name) {
				continue
			}
			add(name, value)
		}
	}

	return lines
}

func hasAllEnvsPrefix(name string) bool {
	for _, prefix := range allEnvsPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func withScriptStop(lines []string) []string {
	stopped := make([]string, 0, 2*len(lines))
	for _, line := range lines {
		stopped = append(stopped, line)
		if !strings.HasSuffix(line, `\`) {
			stopped = append(stopped, scriptStopGuard)
		}
	}
	return stopped
}
