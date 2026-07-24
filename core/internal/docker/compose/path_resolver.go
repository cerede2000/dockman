package compose

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
)

// PathResolver maps an absolute compose-file path (as recorded by the
// daemon in the com.docker.compose.project.config_files label) back to a
// dockman filename ("alias/relpath"). Returns "" when the path lives
// outside every alias root.
type PathResolver func(absPath string) string

// SetPathResolver wires the host-level reverse mapping; the host service
// owns the alias table, so the closure is injected after construction.
func (c *Service) SetPathResolver(resolver PathResolver) {
	c.pathResolver = resolver
}

// DockmanPath resolves a config_files label value to a dockman filename.
// The label can list several files comma-separated; the first one names
// the stack's main compose file.
func (c *Service) DockmanPath(configFilesLabel string) string {
	if c.pathResolver == nil || configFilesLabel == "" {
		return ""
	}
	first := strings.TrimSpace(strings.Split(configFilesLabel, ",")[0])
	if first == "" {
		return ""
	}
	return c.pathResolver(filepath.ToSlash(first))
}

// ComposeFileExists verifies that a Docker-discovered compose path still
// belongs to a real file in the configured host filesystem. Containers can
// outlive a deleted or renamed compose file, so their config-files label alone
// is not an authoritative stack index.
func (c *Service) ComposeFileExists(filename string) (bool, error) {
	parts, err := c.parser(filename, c.hostname)
	if err != nil {
		return false, err
	}

	info, err := parts.Fs.Stat(parts.Relpath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return !info.IsDir(), nil
}
