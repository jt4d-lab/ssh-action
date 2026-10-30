package config_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jt4d-lab/ssh-action/internal/config"
)

func lookup(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}
}

func baseInputs() map[string]string {
	return map[string]string{
		"INPUT_HOST":     "example.com",
		"INPUT_PASSWORD": "secret",
		"INPUT_SCRIPT":   "uptime",
	}
}

func TestFromEnvDefaults(t *testing.T) {
	cfg, err := config.FromEnv(lookup(baseInputs()))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}

	want := &config.Config{
		Host:           "example.com",
		Port:           "22",
		Protocol:       "tcp",
		Username:       "root",
		Password:       "secret",
		Timeout:        30 * time.Second,
		CommandTimeout: 10 * time.Minute,
		EnvsFormat:     "export {NAME}={VALUE}",
		Script:         "uptime",
		Proxy: config.Proxy{
			Port:     "22",
			Protocol: "tcp",
			Timeout:  30 * time.Second,
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("FromEnv defaults mismatch\n got: %+v\nwant: %+v", cfg, want)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	vars := map[string]string{
		"INPUT_HOST":                "h1:2222,h2",
		"INPUT_PORT":                "2200",
		"INPUT_PROTOCOL":            "tcp4",
		"INPUT_USERNAME":            "deploy",
		"INPUT_PASSWORD":            "pw",
		"INPUT_KEY":                 "-----BEGIN KEY-----",
		"INPUT_KEY_PATH":            "/tmp/id_ed25519",
		"INPUT_PASSPHRASE":          "pp",
		"INPUT_FINGERPRINT":         "SHA256:abc",
		"INPUT_TIMEOUT":             "5s",
		"INPUT_CIPHERS":             "aes128-gcm@openssh.com, chacha20-poly1305@openssh.com",
		"INPUT_USE_INSECURE_CIPHER": "true",
		"INPUT_SYNC":                "true",

		"INPUT_PROXY_HOST":                "jump.example.com",
		"INPUT_PROXY_PORT":                "2022",
		"INPUT_PROXY_PROTOCOL":            "tcp6",
		"INPUT_PROXY_USERNAME":            "bastion",
		"INPUT_PROXY_PASSWORD":            "ppw",
		"INPUT_PROXY_KEY":                 "-----BEGIN PROXY KEY-----",
		"INPUT_PROXY_KEY_PATH":            "/tmp/proxy_id",
		"INPUT_PROXY_PASSPHRASE":          "ppp",
		"INPUT_PROXY_FINGERPRINT":         "SHA256:def",
		"INPUT_PROXY_CIPHER":              "3des-cbc",
		"INPUT_PROXY_USE_INSECURE_CIPHER": "true",
		"INPUT_PROXY_TIMEOUT":             "7s",

		"INPUT_SCRIPT":          "uptime",
		"INPUT_SCRIPT_PATH":     "deploy.sh",
		"INPUT_SCRIPT_STOP":     "true",
		"INPUT_COMMAND_TIMEOUT": "90s",
		"INPUT_ENVS":            "FOO,BAR",
		"INPUT_ENVS_FORMAT":     "export {NAME}='{VALUE}'",
		"INPUT_ALLENVS":         "true",
		"INPUT_REQUEST_PTY":     "true",
		"INPUT_DEBUG":           "true",
		"INPUT_CAPTURE_STDOUT":  "true",

		"INPUT_WORKDIR":        "~/app",
		"INPUT_CREATE_WORKDIR": "true",
		"INPUT_FILES":          "dist/*\nREADME.md",
	}

	cfg, err := config.FromEnv(lookup(vars))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}

	want := &config.Config{
		Host:              "h1:2222,h2",
		Port:              "2200",
		Protocol:          "tcp4",
		Username:          "deploy",
		Password:          "pw",
		Key:               "-----BEGIN KEY-----",
		KeyPath:           "/tmp/id_ed25519",
		Passphrase:        "pp",
		Fingerprint:       "SHA256:abc",
		Ciphers:           []string{"aes128-gcm@openssh.com", "chacha20-poly1305@openssh.com"},
		UseInsecureCipher: true,
		Timeout:           5 * time.Second,
		Sync:              true,

		Proxy: config.Proxy{
			Host:              "jump.example.com",
			Port:              "2022",
			Protocol:          "tcp6",
			Username:          "bastion",
			Password:          "ppw",
			Key:               "-----BEGIN PROXY KEY-----",
			KeyPath:           "/tmp/proxy_id",
			Passphrase:        "ppp",
			Fingerprint:       "SHA256:def",
			Ciphers:           []string{"3des-cbc"},
			UseInsecureCipher: true,
			Timeout:           7 * time.Second,
		},

		Script:         "uptime",
		ScriptPath:     "deploy.sh",
		ScriptStop:     true,
		CommandTimeout: 90 * time.Second,
		Envs:           "FOO,BAR",
		EnvsFormat:     "export {NAME}='{VALUE}'",
		AllEnvs:        true,
		RequestPty:     true,
		Debug:          true,
		CaptureStdout:  true,

		Workdir:       "~/app",
		CreateWorkdir: true,
		Files:         "dist/*\nREADME.md",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("FromEnv overrides mismatch\n got: %+v\nwant: %+v", cfg, want)
	}
}

func TestFromEnvCipherNames(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]string
		want []string
	}{
		{
			name: "cipher as emitted by action.yml",
			vars: map[string]string{"INPUT_CIPHER": "aes256-ctr"},
			want: []string{"aes256-ctr"},
		},
		{
			name: "ciphers as read by drone-ssh",
			vars: map[string]string{"INPUT_CIPHERS": "aes256-ctr,aes192-ctr"},
			want: []string{"aes256-ctr", "aes192-ctr"},
		},
		{
			name: "cipher wins over ciphers",
			vars: map[string]string{"INPUT_CIPHER": "aes256-ctr", "INPUT_CIPHERS": "aes128-ctr"},
			want: []string{"aes256-ctr"},
		},
		{
			name: "unset",
			vars: nil,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := baseInputs()
			for k, v := range tt.vars {
				vars[k] = v
			}
			cfg, err := config.FromEnv(lookup(vars))
			if err != nil {
				t.Fatalf("FromEnv: %v", err)
			}
			if !reflect.DeepEqual(cfg.Ciphers, tt.want) {
				t.Errorf("Ciphers = %v, want %v", cfg.Ciphers, tt.want)
			}
		})
	}
}

func TestFromEnvInvalidValue(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		wantMsg string
	}{
		{name: "protocol", vars: map[string]string{"INPUT_PROTOCOL": "udp"}, wantMsg: `invalid protocol "udp"`},
		{name: "proxy protocol", vars: map[string]string{"INPUT_PROXY_PROTOCOL": "unix"}, wantMsg: `invalid protocol "unix"`},
		{name: "port", vars: map[string]string{"INPUT_PORT": "ssh"}, wantMsg: `invalid port "ssh"`},
		{name: "port out of range", vars: map[string]string{"INPUT_PORT": "70000"}, wantMsg: `invalid port "70000"`},
		{name: "proxy port", vars: map[string]string{"INPUT_PROXY_PORT": "-1"}, wantMsg: `invalid port "-1"`},
		{name: "timeout", vars: map[string]string{"INPUT_TIMEOUT": "soon"}, wantMsg: `invalid duration "soon"`},
		{name: "zero timeout", vars: map[string]string{"INPUT_TIMEOUT": "0s"}, wantMsg: `invalid duration "0s"`},
		{name: "command timeout", vars: map[string]string{"INPUT_COMMAND_TIMEOUT": "10"}, wantMsg: `invalid duration "10"`},
		{name: "proxy timeout", vars: map[string]string{"INPUT_PROXY_TIMEOUT": "1 minute"}, wantMsg: `invalid duration "1 minute"`},
		{name: "boolean", vars: map[string]string{"INPUT_SYNC": "yes please"}, wantMsg: `invalid boolean "yes please"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := baseInputs()
			for k, v := range tt.vars {
				vars[k] = v
			}
			_, err := config.FromEnv(lookup(vars))
			if !errors.Is(err, config.ErrInvalidConfig) {
				t.Fatalf("FromEnv error = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("FromEnv error = %q, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestFromEnvValidationRules(t *testing.T) {
	tests := []struct {
		name    string
		remove  []string
		vars    map[string]string
		wantMsg string
	}{
		{
			name:    "missing host",
			remove:  []string{"INPUT_HOST"},
			wantMsg: "missing server host",
		},
		{
			name:    "host with only separators",
			vars:    map[string]string{"INPUT_HOST": " , ,"},
			wantMsg: "missing server host",
		},
		{
			name:    "missing credentials",
			remove:  []string{"INPUT_PASSWORD"},
			wantMsg: "missing password, key or key_path",
		},
		{
			name:    "files without workdir",
			vars:    map[string]string{"INPUT_FILES": "dist/*"},
			wantMsg: "files requires workdir",
		},
		{
			name:    "create_workdir without workdir",
			vars:    map[string]string{"INPUT_CREATE_WORKDIR": "true"},
			wantMsg: "create_workdir requires workdir",
		},
		{
			name:    "foreign home in workdir",
			vars:    map[string]string{"INPUT_WORKDIR": "~bob/app"},
			wantMsg: "only the current user home",
		},
		{
			name:    "empty inputs",
			remove:  []string{"INPUT_SCRIPT"},
			wantMsg: "nothing to execute",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := baseInputs()
			for k, v := range tt.vars {
				vars[k] = v
			}
			for _, k := range tt.remove {
				delete(vars, k)
			}
			_, err := config.FromEnv(lookup(vars))
			if !errors.Is(err, config.ErrInvalidConfig) {
				t.Fatalf("FromEnv error = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("FromEnv error = %q, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestFromEnvCopyOnlyMode(t *testing.T) {
	vars := baseInputs()
	delete(vars, "INPUT_SCRIPT")
	vars["INPUT_WORKDIR"] = "/srv/app"
	vars["INPUT_FILES"] = "dist/*"

	cfg, err := config.FromEnv(lookup(vars))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.Script != "" || cfg.Files == "" {
		t.Errorf("copy-only config = %+v, want empty script and files set", cfg)
	}
}

func TestFromEnvAcceptsKeyPathOnly(t *testing.T) {
	vars := baseInputs()
	delete(vars, "INPUT_PASSWORD")
	vars["INPUT_KEY_PATH"] = "~/.ssh/id_ed25519"

	if _, err := config.FromEnv(lookup(vars)); err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
}

func TestValidateDirect(t *testing.T) {
	valid := config.Config{Host: "example.com", Password: "p", Script: "ls"}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate(valid) = %v, want nil", err)
	}

	invalid := config.Config{Host: "example.com", Password: "p"}
	if err := invalid.Validate(); !errors.Is(err, config.ErrInvalidConfig) {
		t.Errorf("Validate(empty) = %v, want ErrInvalidConfig", err)
	}
}

func TestDebugDumpRedactsCredentials(t *testing.T) {
	cfg := &config.Config{
		Host:        "example.com",
		Port:        "22",
		Protocol:    "tcp",
		Username:    "deploy",
		Password:    "hunter2-password",
		Key:         "PRIVATE-KEY-MATERIAL",
		KeyPath:     "/home/deploy/.ssh/id_ed25519",
		Passphrase:  "key-passphrase",
		Fingerprint: "SHA256:abc",
		Timeout:     30 * time.Second,
		Proxy: config.Proxy{
			Host:       "jump.example.com",
			Port:       "22",
			Protocol:   "tcp",
			Username:   "jump",
			Password:   "proxy-password",
			Key:        "PROXY-KEY-MATERIAL",
			Passphrase: "proxy-passphrase",
			Timeout:    30 * time.Second,
		},
		Script:         "deploy.sh --now",
		CommandTimeout: time.Minute,
		Envs:           "TOKEN,API_KEY",
		EnvsFormat:     "export {NAME}={VALUE}",
		Workdir:        "/srv/app",
		CreateWorkdir:  true,
		Files:          "dist/*\nbin/tool",
	}

	dump := cfg.DebugDump()
	for _, secret := range []string{
		"hunter2-password", "PRIVATE-KEY-MATERIAL", "key-passphrase",
		"proxy-password", "PROXY-KEY-MATERIAL", "proxy-passphrase",
	} {
		if strings.Contains(dump, secret) {
			t.Errorf("dump leaks %q:\n%s", secret, dump)
		}
	}
	for _, want := range []string{
		"example.com",
		"password: [REDACTED]",
		"key: [REDACTED]",
		"passphrase: [REDACTED]",
		"proxy_password: [REDACTED]",
		"proxy_key: [REDACTED]",
		"proxy_passphrase: [REDACTED]",
		"envs: TOKEN,API_KEY",
		"workdir: /srv/app",
		"dist/*",
		"bin/tool",
		"deploy.sh --now",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump lacks %q:\n%s", want, dump)
		}
	}
}
