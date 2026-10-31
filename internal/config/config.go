// Package config turns ssh-action inputs into a validated Config.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidConfig reports an input error that must map to exit code 2.
var ErrInvalidConfig = errors.New("invalid configuration")

// Defaults applied to inputs that are not set.
const (
	DefaultPort           = "22"
	DefaultProtocol       = "tcp"
	DefaultUsername       = "root"
	DefaultTimeout        = 30 * time.Second
	DefaultCommandTimeout = 10 * time.Minute
	DefaultEnvsFormat     = "export {NAME}={VALUE}"

	DefaultProxyPort     = "22"
	DefaultProxyProtocol = "tcp"
	DefaultProxyTimeout  = 30 * time.Second
)

// Proxy holds the jump-host settings of an optional bastion connection.
type Proxy struct {
	Host              string
	Port              string
	Protocol          string
	Username          string
	Password          string
	Key               string
	KeyPath           string
	Passphrase        string
	Fingerprint       string
	Ciphers           []string
	UseInsecureCipher bool
	Timeout           time.Duration
}

// Config is the complete set of action inputs.
type Config struct {
	Host              string
	Port              string
	Protocol          string
	Username          string
	Password          string
	Key               string
	KeyPath           string
	Passphrase        string
	Fingerprint       string
	Ciphers           []string
	UseInsecureCipher bool
	Timeout           time.Duration
	Sync              bool

	Proxy Proxy

	Script         string
	ScriptPath     string
	ScriptStop     bool
	CommandTimeout time.Duration
	Envs           string
	EnvsFormat     string
	AllEnvs        bool
	RequestPty     bool
	Debug          bool
	CaptureStdout  bool

	Workdir       string
	CreateWorkdir bool
	Files         string
}

// FromEnv builds a Config from the action inputs read through lookup, which
// reports false for variables that are unset. The returned error wraps
// ErrInvalidConfig for every violated input rule.
func FromEnv(lookup func(string) (string, bool)) (*Config, error) {
	var errs []error

	str := func(name, fallback string) string {
		v, ok := lookup(name)
		if !ok || strings.TrimSpace(v) == "" {
			return fallback
		}
		return v
	}
	boolean := func(name string) bool {
		v, ok := lookup(name)
		if !ok || strings.TrimSpace(v) == "" {
			return false
		}
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, Invalidf("invalid boolean %q in %s", v, name))
			return false
		}
		return b
	}
	duration := func(name string, fallback time.Duration) time.Duration {
		v, ok := lookup(name)
		if !ok || strings.TrimSpace(v) == "" {
			return fallback
		}
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil || d <= 0 {
			errs = append(errs, Invalidf("invalid duration %q in %s", v, name))
			return fallback
		}
		return d
	}
	port := func(name, fallback string) string {
		v := strings.TrimSpace(str(name, fallback))
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			errs = append(errs, Invalidf("invalid port %q in %s", v, name))
		}
		return v
	}
	protocol := func(name, fallback string) string {
		v := strings.ToLower(strings.TrimSpace(str(name, fallback)))
		switch v {
		case "tcp", "tcp4", "tcp6":
		default:
			errs = append(errs, Invalidf("invalid protocol %q in %s: want tcp, tcp4 or tcp6", v, name))
		}
		return v
	}
	list := func(names ...string) []string {
		for _, name := range names {
			v := str(name, "")
			if v == "" {
				continue
			}
			var items []string
			for _, item := range strings.Split(v, ",") {
				if item = strings.TrimSpace(item); item != "" {
					items = append(items, item)
				}
			}
			return items
		}
		return nil
	}

	cfg := &Config{
		Host:              strings.TrimSpace(str("INPUT_HOST", "")),
		Port:              port("INPUT_PORT", DefaultPort),
		Protocol:          protocol("INPUT_PROTOCOL", DefaultProtocol),
		Username:          str("INPUT_USERNAME", DefaultUsername),
		Password:          str("INPUT_PASSWORD", ""),
		Key:               str("INPUT_KEY", ""),
		KeyPath:           str("INPUT_KEY_PATH", ""),
		Passphrase:        str("INPUT_PASSPHRASE", ""),
		Fingerprint:       str("INPUT_FINGERPRINT", ""),
		Ciphers:           list("INPUT_CIPHER", "INPUT_CIPHERS"),
		UseInsecureCipher: boolean("INPUT_USE_INSECURE_CIPHER"),
		Timeout:           duration("INPUT_TIMEOUT", DefaultTimeout),
		Sync:              boolean("INPUT_SYNC"),

		Proxy: Proxy{
			Host:              strings.TrimSpace(str("INPUT_PROXY_HOST", "")),
			Port:              port("INPUT_PROXY_PORT", DefaultProxyPort),
			Protocol:          protocol("INPUT_PROXY_PROTOCOL", DefaultProxyProtocol),
			Username:          str("INPUT_PROXY_USERNAME", ""),
			Password:          str("INPUT_PROXY_PASSWORD", ""),
			Key:               str("INPUT_PROXY_KEY", ""),
			KeyPath:           str("INPUT_PROXY_KEY_PATH", ""),
			Passphrase:        str("INPUT_PROXY_PASSPHRASE", ""),
			Fingerprint:       str("INPUT_PROXY_FINGERPRINT", ""),
			Ciphers:           list("INPUT_PROXY_CIPHER"),
			UseInsecureCipher: boolean("INPUT_PROXY_USE_INSECURE_CIPHER"),
			Timeout:           duration("INPUT_PROXY_TIMEOUT", DefaultProxyTimeout),
		},

		Script:         str("INPUT_SCRIPT", ""),
		ScriptPath:     str("INPUT_SCRIPT_PATH", ""),
		ScriptStop:     boolean("INPUT_SCRIPT_STOP"),
		CommandTimeout: duration("INPUT_COMMAND_TIMEOUT", DefaultCommandTimeout),
		Envs:           str("INPUT_ENVS", ""),
		EnvsFormat:     str("INPUT_ENVS_FORMAT", DefaultEnvsFormat),
		AllEnvs:        boolean("INPUT_ALLENVS"),
		RequestPty:     boolean("INPUT_REQUEST_PTY"),
		Debug:          boolean("INPUT_DEBUG"),
		CaptureStdout:  boolean("INPUT_CAPTURE_STDOUT"),

		Workdir:       strings.TrimSpace(str("INPUT_WORKDIR", "")),
		CreateWorkdir: boolean("INPUT_CREATE_WORKDIR"),
		Files:         str("INPUT_FILES", ""),
	}

	if err := cfg.Validate(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

// Validate reports the first violated input rule as an ErrInvalidConfig error.
func (c *Config) Validate() error {
	if !hasHost(c.Host) {
		return Invalidf("missing server host")
	}
	if c.Key == "" && c.KeyPath == "" && c.Password == "" {
		return Invalidf("missing password, key or key_path")
	}
	if strings.HasPrefix(c.Workdir, "~") && c.Workdir != "~" && !strings.HasPrefix(c.Workdir, "~/") {
		return Invalidf(`workdir %q: only the current user home ("~" or "~/...") can be expanded`, c.Workdir)
	}
	if c.Workdir == "" {
		if strings.TrimSpace(c.Files) != "" {
			return Invalidf("files requires workdir")
		}
		if c.CreateWorkdir {
			return Invalidf("create_workdir requires workdir")
		}
	}
	if strings.TrimSpace(c.Script) == "" && strings.TrimSpace(c.ScriptPath) == "" && strings.TrimSpace(c.Files) == "" {
		return Invalidf("nothing to execute: script, script_path and files are empty")
	}
	return nil
}

// redactedSecret replaces every credential of a DebugDump.
const redactedSecret = "[REDACTED]"

// DebugDump renders c for the debug input: credentials are replaced by
// [REDACTED], envs appear as the list of names they are (Config never holds
// their values), and files and script are shown verbatim.
func (c *Config) DebugDump() string {
	fields := []struct{ name, value string }{
		{"host", c.Host},
		{"port", c.Port},
		{"protocol", c.Protocol},
		{"username", c.Username},
		{"password", redacted(c.Password)},
		{"key", redacted(c.Key)},
		{"key_path", c.KeyPath},
		{"passphrase", redacted(c.Passphrase)},
		{"fingerprint", c.Fingerprint},
		{"cipher", strings.Join(c.Ciphers, ",")},
		{"use_insecure_cipher", strconv.FormatBool(c.UseInsecureCipher)},
		{"timeout", c.Timeout.String()},
		{"sync", strconv.FormatBool(c.Sync)},
		{"proxy_host", c.Proxy.Host},
		{"proxy_port", c.Proxy.Port},
		{"proxy_protocol", c.Proxy.Protocol},
		{"proxy_username", c.Proxy.Username},
		{"proxy_password", redacted(c.Proxy.Password)},
		{"proxy_key", redacted(c.Proxy.Key)},
		{"proxy_key_path", c.Proxy.KeyPath},
		{"proxy_passphrase", redacted(c.Proxy.Passphrase)},
		{"proxy_fingerprint", c.Proxy.Fingerprint},
		{"proxy_cipher", strings.Join(c.Proxy.Ciphers, ",")},
		{"proxy_use_insecure_cipher", strconv.FormatBool(c.Proxy.UseInsecureCipher)},
		{"proxy_timeout", c.Proxy.Timeout.String()},
		{"script_path", c.ScriptPath},
		{"script_stop", strconv.FormatBool(c.ScriptStop)},
		{"command_timeout", c.CommandTimeout.String()},
		{"envs", c.Envs},
		{"envs_format", c.EnvsFormat},
		{"allenvs", strconv.FormatBool(c.AllEnvs)},
		{"request_pty", strconv.FormatBool(c.RequestPty)},
		{"capture_stdout", strconv.FormatBool(c.CaptureStdout)},
		{"debug", strconv.FormatBool(c.Debug)},
		{"workdir", c.Workdir},
		{"create_workdir", strconv.FormatBool(c.CreateWorkdir)},
	}

	var b strings.Builder
	b.WriteString("ssh-action configuration:\n")
	for _, f := range fields {
		_, _ = fmt.Fprintf(&b, "  %s: %s\n", f.name, f.value)
	}
	writeBlock(&b, "files", c.Files)
	writeBlock(&b, "script", c.Script)
	return b.String()
}

// writeBlock appends value under name as an indented block, one line per entry.
func writeBlock(b *strings.Builder, name, value string) {
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return
	}
	_, _ = fmt.Fprintf(b, "  %s:\n", name)
	for _, line := range lines {
		_, _ = fmt.Fprintf(b, "    %s\n", line)
	}
}

func redacted(secret string) string {
	if secret == "" {
		return ""
	}
	return redactedSecret
}

func hasHost(list string) bool {
	for _, host := range strings.Split(list, ",") {
		if strings.TrimSpace(host) != "" {
			return true
		}
	}
	return false
}

// Invalidf returns an error that wraps ErrInvalidConfig with the message
// formatted from format and args.
func Invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
}
