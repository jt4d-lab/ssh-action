// Package sshx connects to hosts, runs the remote script and uploads files.
package sshx

import (
	"fmt"
	"net"

	easyssh "github.com/appleboy/easyssh-proxy"
	"golang.org/x/crypto/ssh"

	"github.com/jt4d-lab/ssh-action/internal/config"
)

// dial opens the single SSH connection a host run shares between its steps. The
// session returned by easyssh is closed right away: only the *ssh.Client is
// kept, so a PTY for request_pty is requested on the script session alone
// instead of on a session that is never used.
func dial(cfg *config.Config, host config.Host) (*ssh.Client, error) {
	makeConfig := &easyssh.MakeConfig{
		User:              cfg.Username,
		Server:            host.Addr,
		Port:              host.Port,
		Protocol:          easyssh.Protocol(cfg.Protocol),
		Password:          cfg.Password,
		Key:               cfg.Key,
		KeyPath:           cfg.KeyPath,
		Passphrase:        cfg.Passphrase,
		Fingerprint:       cfg.Fingerprint,
		Ciphers:           cfg.Ciphers,
		UseInsecureCipher: cfg.UseInsecureCipher,
		Timeout:           cfg.Timeout,
		Proxy: easyssh.DefaultConfig{
			User:              cfg.Proxy.Username,
			Server:            cfg.Proxy.Host,
			Port:              cfg.Proxy.Port,
			Protocol:          easyssh.Protocol(cfg.Proxy.Protocol),
			Password:          cfg.Proxy.Password,
			Key:               cfg.Proxy.Key,
			KeyPath:           cfg.Proxy.KeyPath,
			Passphrase:        cfg.Proxy.Passphrase,
			Fingerprint:       cfg.Proxy.Fingerprint,
			Ciphers:           cfg.Proxy.Ciphers,
			UseInsecureCipher: cfg.Proxy.UseInsecureCipher,
			Timeout:           cfg.Proxy.Timeout,
		},
	}

	session, client, err := makeConfig.Connect()
	if err != nil {
		return nil, fmt.Errorf("host %s: connect: %w", hostName(host), err)
	}
	_ = session.Close()
	return client, nil
}

// hostName is how a host appears in log and error messages.
func hostName(host config.Host) string {
	return net.JoinHostPort(host.Addr, host.Port)
}
