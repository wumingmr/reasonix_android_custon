package config

import (
	"os"
	"path/filepath"
	"strings"
)

// configPath resolves against the actual workspace, never a project-declared
// workspace_root. Existing link identity is resolved before containment checks.
func (c *Config) configPath(ws, raw string) (string, bool) {
	p := strings.TrimSpace(c.expandSandboxPath(raw))
	if p == "" || ws == "" || strings.Contains(p, "${") {
		return "", false
	}
	if filepath.VolumeName(p) == "" && !os.IsPathSeparator(p[0]) {
		p = filepath.Join(ws, p)
	} else if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p, err := evalSymlinksAllowMissing(p)
	return p, err == nil
}

func (c *Config) equivalentConfigPath(ws, a, b string) bool {
	aa, aok := c.configPath(ws, a)
	bb, bok := c.configPath(ws, b)
	if !aok || !bok {
		return false
	}
	if aa == bb {
		return true
	}
	ai, ae := os.Stat(aa)
	bi, be := os.Stat(bb)
	return ae == nil && be == nil && os.SameFile(ai, bi)
}

func (c *Config) authorizedPath(ws string, allowed []string, candidate string) bool {
	p, ok := c.configPath(ws, candidate)
	if !ok {
		return false
	}
	for _, raw := range allowed {
		root, ok := c.configPath(ws, raw)
		if ok && configDirectoryCovers(root, p) {
			return true
		}
	}
	return false
}

// Prefer directory identity on all filesystems, including Windows directories
// with case sensitivity enabled. filepath.Rel alone folds Windows case even
// when the volume does not. Missing grant roots require exact component names.
func configDirectoryCovers(root, path string) bool {
	if info, err := os.Stat(root); err == nil {
		return info.IsDir() && sameFileAncestor(root, path)
	}
	for dir := path; ; dir = filepath.Dir(dir) {
		if dir == root {
			return true
		}
		if filepath.Dir(dir) == dir {
			return false
		}
	}
}
