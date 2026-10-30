package config_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jt4d-lab/ssh-action/internal/config"
)

func TestParseHosts(t *testing.T) {
	tests := []struct {
		name     string
		list     string
		port     string
		protocol string
		want     []config.Host
	}{
		{
			name:     "comma separated list",
			list:     "h1,h2",
			port:     "22",
			protocol: "tcp",
			want:     []config.Host{{Addr: "h1", Port: "22"}, {Addr: "h2", Port: "22"}},
		},
		{
			name:     "spaces and empty elements",
			list:     " h1 , , h2 ,",
			port:     "22",
			protocol: "tcp",
			want:     []config.Host{{Addr: "h1", Port: "22"}, {Addr: "h2", Port: "22"}},
		},
		{
			name:     "port in the element wins over the input port",
			list:     "h1:2222",
			port:     "2200",
			protocol: "tcp",
			want:     []config.Host{{Addr: "h1", Port: "2222"}},
		},
		{
			name:     "empty port after the colon falls back to the input port",
			list:     "h1:",
			port:     "22",
			protocol: "tcp",
			want:     []config.Host{{Addr: "h1", Port: "22"}},
		},
		{
			name:     "tcp4 with port",
			list:     "10.0.0.1:2200",
			port:     "22",
			protocol: "tcp4",
			want:     []config.Host{{Addr: "10.0.0.1", Port: "2200"}},
		},
		{
			name:     "bracketed ipv6 with port",
			list:     "[::1]:2222",
			port:     "22",
			protocol: "tcp",
			want:     []config.Host{{Addr: "::1", Port: "2222"}},
		},
		{
			name:     "tcp6 element is never split",
			list:     "[::1]:2222",
			port:     "2200",
			protocol: "tcp6",
			want:     []config.Host{{Addr: "[::1]:2222", Port: "2200"}},
		},
		{
			name:     "tcp6 bare literal is not split",
			list:     "::1",
			port:     "22",
			protocol: "tcp6",
			want:     []config.Host{{Addr: "::1", Port: "22"}},
		},
		{
			name:     "duplicates are kept",
			list:     "h1,h1",
			port:     "22",
			protocol: "tcp",
			want:     []config.Host{{Addr: "h1", Port: "22"}, {Addr: "h1", Port: "22"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := config.ParseHosts(tt.list, tt.port, tt.protocol)
			if err != nil {
				t.Fatalf("ParseHosts: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseHosts = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseHostsInvalid(t *testing.T) {
	tests := []struct {
		name     string
		list     string
		protocol string
		wantMsg  string
	}{
		{
			name:     "bare ipv6 literal with tcp",
			list:     "::1",
			protocol: "tcp",
			wantMsg:  "IPv6 literal in brackets",
		},
		{
			name:     "bare ipv6 literal among valid hosts with tcp4",
			list:     "h1,fe80::1:2222",
			protocol: "tcp4",
			wantMsg:  "IPv6 literal in brackets",
		},
		{
			name:     "bracketed ipv6 without port",
			list:     "[::1]",
			protocol: "tcp",
			wantMsg:  "IPv6 literal in brackets",
		},
		{
			name:     "empty list",
			list:     "",
			protocol: "tcp",
			wantMsg:  "missing server host",
		},
		{
			name:     "only separators",
			list:     " , , ",
			protocol: "tcp",
			wantMsg:  "missing server host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.ParseHosts(tt.list, "22", tt.protocol)
			if !errors.Is(err, config.ErrInvalidConfig) {
				t.Fatalf("ParseHosts error = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("ParseHosts error = %q, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}
