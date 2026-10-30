// Package script quotes paths and assembles the remote shell script.
package script

import (
	"fmt"
	"strings"

	"github.com/jt4d-lab/ssh-action/internal/config"
)

// QuotePath returns p quoted for a POSIX shell running on the host. A leading
// "~" or "~/" becomes "$HOME" so it is expanded on the host; any other path is
// single quoted verbatim. An empty path and the "~user" form, which would
// expand another user's home directory, are rejected with an error wrapping
// config.ErrInvalidConfig.
func QuotePath(p string) (string, error) {
	switch {
	case p == "":
		return "", invalidf("empty path")
	case p == "~":
		return `"$HOME"`, nil
	case strings.HasPrefix(p, "~/"):
		if rest := p[2:]; rest != "" {
			return `"$HOME"/` + singleQuote(rest), nil
		}
		return `"$HOME"`, nil
	case strings.HasPrefix(p, "~"):
		return "", invalidf("path %q: only the current user home (%q or %q) can be expanded", p, "~", "~/...")
	}
	return singleQuote(p), nil
}

func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", config.ErrInvalidConfig, fmt.Sprintf(format, args...))
}
