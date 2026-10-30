package config

import (
	"net"
	"strings"
)

// Host is one target of a run: Addr is dialed on Port.
type Host struct {
	Addr string
	Port string
}

// ParseHosts splits the comma separated host input into targets, trimming
// spaces and dropping empty elements. For tcp and tcp4 an element of the form
// host:port overrides defaultPort and an IPv6 literal must be bracketed
// ([::1]:2222); for tcp6 an element is never split and defaultPort is always
// used. The returned error wraps ErrInvalidConfig.
func ParseHosts(list, defaultPort, protocol string) ([]Host, error) {
	var hosts []Host
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		host, err := parseHost(item, defaultPort, protocol)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	if len(hosts) == 0 {
		return nil, Invalidf("missing server host")
	}
	return hosts, nil
}

func parseHost(item, defaultPort, protocol string) (Host, error) {
	if protocol == "tcp6" || !strings.Contains(item, ":") {
		return Host{Addr: item, Port: defaultPort}, nil
	}
	addr, port, err := net.SplitHostPort(item)
	if err != nil {
		return Host{}, Invalidf("invalid host %q: want host or host:port, with an IPv6 literal in brackets like [::1]:2222", item)
	}
	if port == "" {
		port = defaultPort
	}
	return Host{Addr: addr, Port: port}, nil
}
