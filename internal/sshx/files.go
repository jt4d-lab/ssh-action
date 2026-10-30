package sshx

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jt4d-lab/ssh-action/internal/config"
)

// workspaceEnv names the variable that relative files patterns resolve against.
const workspaceEnv = "GITHUB_WORKSPACE"

// MatchFiles expands the newline separated filepath.Glob patterns of files into
// the local paths to upload, resolving relative patterns against
// GITHUB_WORKSPACE read through lookup, or the process working directory when
// lookup is nil or the variable is unset. "**" is not supported. A malformed
// pattern and a pattern that matches nothing are errors wrapping
// config.ErrInvalidConfig, so a deploy never succeeds without copying anything.
// Entries that share a basename collapse to the last match, because the basename
// is the name an entry takes on the host.
func MatchFiles(files string, lookup func(string) (string, bool)) ([]string, error) {
	root := ""
	if lookup != nil {
		if workspace, ok := lookup(workspaceEnv); ok {
			root = strings.TrimSpace(workspace)
		}
	}

	var paths []string
	position := make(map[string]int)
	for _, pattern := range strings.Split(files, "\n") {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if !filepath.IsAbs(pattern) && root != "" {
			pattern = filepath.Join(root, pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, config.Invalidf("files %q: %v", pattern, err)
		}
		if len(matches) == 0 {
			return nil, config.Invalidf("files %q: no match", pattern)
		}
		for _, match := range matches {
			base := filepath.Base(match)
			if i, ok := position[base]; ok {
				paths[i] = match
				continue
			}
			position[base] = len(paths)
			paths = append(paths, match)
		}
	}
	return paths, nil
}

// writeTar streams entries to w as a GNU tar archive. GNU headers are used
// because busybox tar understands GNU long names but not the whole PAX format.
// A top level entry is dereferenced; symlinks below it are stored as symlinks.
// Entries that are neither regular files, directories nor symlinks are skipped
// and reported to warn.
func writeTar(w io.Writer, entries []string, warn io.Writer) error {
	tw := tar.NewWriter(w)
	for _, entry := range entries {
		if err := addToTar(tw, entry, filepath.Base(entry), warn, true); err != nil {
			return err
		}
	}
	return tw.Close()
}

// addToTar writes path, and for a directory its contents, to tw under name.
func addToTar(tw *tar.Writer, path, name string, warn io.Writer, top bool) error {
	var (
		info os.FileInfo
		err  error
	)
	if top {
		info, err = os.Stat(path)
	} else {
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}

	var link string
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		if link, err = os.Readlink(path); err != nil {
			return err
		}
	case !mode.IsDir() && !mode.IsRegular():
		if warn != nil {
			_, _ = fmt.Fprintf(warn, "warning: skip %s: unsupported file type\n", path)
		}
		return nil
	}

	hdr, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	hdr.Format = tar.FormatGNU
	hdr.Name = name
	if info.IsDir() {
		hdr.Name += "/"
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}

	if info.IsDir() {
		children, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, child := range children {
			childPath := filepath.Join(path, child.Name())
			if err := addToTar(tw, childPath, name+"/"+child.Name(), warn, false); err != nil {
				return err
			}
		}
		return nil
	}
	if hdr.Typeflag != tar.TypeReg {
		return nil
	}
	return copyToTar(tw, path)
}

func copyToTar(tw *tar.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = io.Copy(tw, file)
	return err
}
