// Command ssh-action runs the remote script and copies local files to every
// host named by the action inputs.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/jt4d-lab/ssh-action/internal/config"
	"github.com/jt4d-lab/ssh-action/internal/script"
	"github.com/jt4d-lab/ssh-action/internal/sshx"
)

// version is the released tag, set by the build with
// -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

// stdoutFileEnv names the file that receives a copy of the remote stdout. The
// action entrypoint sets it for capture_stdout, so it is not an action input.
const stdoutFileEnv = "SSH_ACTION_STDOUT_FILE"

// Exit codes of the action contract: 2 is a broken configuration detected
// before any connection, 1 is a failed connection, upload or command.
const (
	exitOK      = 0
	exitFailure = 1
	exitConfig  = 2
)

// flagInputs are the action inputs that command line flags can override. A flag
// name drops the INPUT_ prefix and lower-cases the rest.
var flagInputs = []string{
	"INPUT_HOST",
	"INPUT_PORT",
	"INPUT_PROTOCOL",
	"INPUT_USERNAME",
	"INPUT_PASSWORD",
	"INPUT_KEY",
	"INPUT_KEY_PATH",
	"INPUT_PASSPHRASE",
	"INPUT_FINGERPRINT",
	"INPUT_CIPHER",
	"INPUT_USE_INSECURE_CIPHER",
	"INPUT_TIMEOUT",
	"INPUT_SYNC",
	"INPUT_PROXY_HOST",
	"INPUT_PROXY_PORT",
	"INPUT_PROXY_PROTOCOL",
	"INPUT_PROXY_USERNAME",
	"INPUT_PROXY_PASSWORD",
	"INPUT_PROXY_KEY",
	"INPUT_PROXY_KEY_PATH",
	"INPUT_PROXY_PASSPHRASE",
	"INPUT_PROXY_FINGERPRINT",
	"INPUT_PROXY_CIPHER",
	"INPUT_PROXY_USE_INSECURE_CIPHER",
	"INPUT_PROXY_TIMEOUT",
	"INPUT_SCRIPT",
	"INPUT_SCRIPT_PATH",
	"INPUT_SCRIPT_STOP",
	"INPUT_COMMAND_TIMEOUT",
	"INPUT_ENVS",
	"INPUT_ENVS_FORMAT",
	"INPUT_ALLENVS",
	"INPUT_REQUEST_PTY",
	"INPUT_DEBUG",
	"INPUT_CAPTURE_STDOUT",
	"INPUT_WORKDIR",
	"INPUT_CREATE_WORKDIR",
	"INPUT_FILES",
}

// boolInputs are the action inputs holding true or false; their flags are
// switches that take no argument.
var boolInputs = map[string]bool{
	"INPUT_USE_INSECURE_CIPHER":       true,
	"INPUT_SYNC":                      true,
	"INPUT_PROXY_USE_INSECURE_CIPHER": true,
	"INPUT_SCRIPT_STOP":               true,
	"INPUT_ALLENVS":                   true,
	"INPUT_REQUEST_PTY":               true,
	"INPUT_DEBUG":                     true,
	"INPUT_CAPTURE_STDOUT":            true,
	"INPUT_CREATE_WORKDIR":            true,
}

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

// run executes one action invocation and returns its exit code. lookup reads the
// action inputs and args are command line flags overriding them, which lets the
// binary run by hand for debugging. Only the remote stdout reaches stdout;
// service messages and the remote stderr go to stderr.
func run(args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) int {
	inputs, showVersion, err := parseFlags(args, lookup, stderr)
	if err != nil {
		return exitConfig
	}
	if showVersion {
		_, _ = fmt.Fprintf(stdout, "ssh-action %s\n", version)
		return exitOK
	}

	cfg, err := config.FromEnv(inputs)
	if err != nil {
		return fail(stderr, exitConfig, err)
	}
	if cfg.Debug {
		_, _ = fmt.Fprint(stderr, cfg.DebugDump())
	}

	hosts, err := config.ParseHosts(cfg.Host, cfg.Port, cfg.Protocol)
	if err != nil {
		return fail(stderr, exitConfig, err)
	}

	entries, err := sshx.MatchFiles(cfg.Files, lookup)
	if err != nil {
		return fail(stderr, exitCode(err), err)
	}

	scriptText, err := script.Build(cfg, inputEnv{lookup: lookup}, stderr)
	if err != nil {
		return fail(stderr, exitCode(err), err)
	}

	remoteStdout, closeStdout, err := stdoutFile(stdout, lookup)
	if err != nil {
		return fail(stderr, exitFailure, err)
	}
	defer closeStdout()

	if err := sshx.Run(cfg, hosts, scriptText, entries, remoteStdout, stderr); err != nil {
		return fail(stderr, exitFailure, err)
	}
	return exitOK
}

// parseFlags reads args and returns the input lookup that prefers a flag which
// was given explicitly over the environment, and whether --version was asked
// for. Parse errors are reported to stderr.
func parseFlags(args []string, lookup func(string) (string, bool), stderr io.Writer) (func(string) (string, bool), bool, error) {
	fs := flag.NewFlagSet("ssh-action", flag.ContinueOnError)
	fs.SetOutput(stderr)

	flags := make(map[string]func() string, len(flagInputs))
	for _, input := range flagInputs {
		name := flagName(input)
		if boolInputs[input] {
			value := fs.Bool(name, false, "override "+input)
			flags[input] = func() string { return strconv.FormatBool(*value) }
			continue
		}
		value := fs.String(name, "", "override "+input)
		flags[input] = func() string { return *value }
	}
	showVersion := fs.Bool("version", false, "print the version and exit")

	if err := fs.Parse(args); err != nil {
		return nil, false, err
	}
	if fs.NArg() > 0 {
		err := fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
		_, _ = fmt.Fprintln(stderr, err)
		return nil, false, err
	}

	set := make(map[string]bool, len(flags))
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	inputs := func(name string) (string, bool) {
		if value, ok := flags[name]; ok && set[flagName(name)] {
			return value(), true
		}
		return lookup(name)
	}
	return inputs, *showVersion, nil
}

func flagName(input string) string {
	return strings.ToLower(strings.TrimPrefix(input, "INPUT_"))
}

// stdoutFile returns the writer the remote stdout goes to, duplicating it into
// SSH_ACTION_STDOUT_FILE when the entrypoint set that variable for
// capture_stdout, and the function that closes the file.
func stdoutFile(stdout io.Writer, lookup func(string) (string, bool)) (io.Writer, func(), error) {
	path, ok := lookup(stdoutFileEnv)
	if !ok || strings.TrimSpace(path) == "" {
		return stdout, func() {}, nil
	}
	file, err := os.Create(strings.TrimSpace(path))
	if err != nil {
		return nil, nil, fmt.Errorf("capture stdout: %w", err)
	}
	return io.MultiWriter(stdout, file), func() { _ = file.Close() }, nil
}

// inputEnv adapts an input lookup to script.Env. allenvs reads the process
// environment, because it forwards the variables of the runner that started the
// binary rather than the inputs of this action alone.
type inputEnv struct {
	lookup func(string) (string, bool)
}

func (e inputEnv) Lookup(name string) (string, bool) { return e.lookup(name) }

func (e inputEnv) Environ() []string { return os.Environ() }

func exitCode(err error) int {
	if errors.Is(err, config.ErrInvalidConfig) {
		return exitConfig
	}
	return exitFailure
}

func fail(stderr io.Writer, code int, err error) int {
	_, _ = fmt.Fprintf(stderr, "ssh-action: %v\n", err)
	return code
}
