package skill

import (
	"fmt"

	"os"

	"path/filepath"

	"strings"

	"reasonix/internal/fileutil"
)

func (s *Store) Create(name string, scope Scope) (string, error) {
	return s.CreateWithContent(name, scope, stubBody(name))
}

// CreateWithContent writes caller-supplied file contents as a canonical
// <name>/SKILL.md skill, refusing to clobber an existing directory-layout or
// legacy flat skill of the same name. Returns the written path.
func (s *Store) CreateWithContent(name string, scope Scope, content string) (string, error) {
	if !IsValidName(name) {
		return "", fmt.Errorf("invalid skill name %q — use letters, digits, '_', '-', '.'", name)
	}
	var root string
	switch scope {
	case ScopeProject:
		if s.projectRoot == "" {
			return "", fmt.Errorf("project scope requires a workspace — run from a project directory, or use global scope")
		}
		root = filepath.Join(s.projectRoot, ".reasonix", SkillsDirname)
	default:
		root = s.globalSkillsRoot()
	}
	flat := filepath.Join(root, name+".md")
	folder := filepath.Join(root, name, SkillFile)
	if _, err := os.Stat(flat); err == nil {
		return "", fmt.Errorf("skill %q already exists at %s", name, flat)
	}
	if _, err := os.Stat(folder); err == nil {
		return "", fmt.Errorf("skill %q already exists at %s", name, folder)
	}
	if err := os.MkdirAll(filepath.Dir(folder), 0o755); err != nil {
		return "", err
	}
	// O_EXCL so a concurrent create (or an existing file) is reported, not clobbered.
	f, err := os.OpenFile(folder, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("skill %q already exists at %s", name, folder)
		}
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	s.Invalidate("create")
	return folder, nil
}

// UpdateContent overwrites an existing user-authored skill's file contents in
// place. Refuses built-ins and a scope mismatch, mirroring Delete's rules —
// see Delete for why a mismatch must refuse rather than silently target the
// wrong file.
func (s *Store) UpdateContent(name string, scope Scope, content string) error {
	if scope == ScopeBuiltin {
		return fmt.Errorf("skill %q is built in and cannot be edited", name)
	}
	sk, ok := s.Read(name)
	if !ok {
		return fmt.Errorf("skill %q not found", name)
	}
	if sk.Scope != scope {
		return fmt.Errorf("skill %q resolves at scope %q, not %q — refusing to edit a different scope's file", name, sk.Scope, scope)
	}
	if sk.Path == "" || sk.Path == "(builtin)" {
		return fmt.Errorf("skill %q has no file to update", name)
	}
	if err := s.validateMutablePath(sk.Path, scope); err != nil {
		return fmt.Errorf("skill %q cannot be edited: %w", name, err)
	}
	info, err := os.Stat(sk.Path)
	if err != nil {
		return err
	}
	if err := fileutil.AtomicWriteFile(sk.Path, []byte(content), info.Mode().Perm()); err != nil {
		return err
	}
	s.Invalidate("update")
	return nil
}

// validateMutablePath rejects writes through linked files or directories. Skill
// discovery intentionally follows symlinks for read compatibility, but editing
// one must never replace content outside the configured scope root.
func (s *Store) validateMutablePath(path string, scope Scope) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for _, root := range s.roots() {
		if root.Scope != scope {
			continue
		}
		absRoot, err := filepath.Abs(root.Dir)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absRoot, absPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		current := absRoot
		parts := []string{"."}
		if rel != "." {
			parts = strings.Split(rel, string(filepath.Separator))
		}
		for _, part := range parts {
			if part != "." {
				current = filepath.Join(current, part)
			}
			info, err := os.Lstat(current)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("path uses symbolic link %s", current)
			}
		}
		realRoot, err := filepath.EvalSymlinks(absRoot)
		if err != nil {
			return err
		}
		realPath, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			return err
		}
		realRel, err := filepath.Rel(realRoot, realPath)
		if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("resolved path is outside scope root %s", absRoot)
		}
		return nil
	}
	return fmt.Errorf("path is outside configured %s skill roots", scope)
}

// Delete removes a user-authored skill. Refuses built-ins (no file backs
// them) and refuses when the resolved skill's actual scope doesn't match the
// requested one — e.g. a project-scope delete for a name that only resolves
// at global scope, which would otherwise silently no-op against the wrong
// file while a same-named project-scope shadow kept showing up in List().
func (s *Store) Delete(name string, scope Scope) error {
	if scope == ScopeBuiltin {
		return fmt.Errorf("skill %q is built in and cannot be deleted", name)
	}
	sk, ok := s.Read(name)
	if !ok {
		return fmt.Errorf("skill %q not found", name)
	}
	if sk.Scope != scope {
		return fmt.Errorf("skill %q resolves at scope %q, not %q — refusing to delete a different scope's file", name, sk.Scope, scope)
	}
	if sk.Path == "" || sk.Path == "(builtin)" {
		return fmt.Errorf("skill %q has no file to delete", name)
	}
	if filepath.Base(sk.Path) == SkillFile {
		if err := os.RemoveAll(filepath.Dir(sk.Path)); err != nil {
			return err
		}
		s.Invalidate("delete")
		return nil
	}
	if err := os.Remove(sk.Path); err != nil {
		return err
	}
	s.Invalidate("delete")
	return nil
}

func (s *Store) globalSkillsRoot() string {
	if s.reasonixHomeDir != "" {
		return filepath.Join(s.reasonixHomeDir, SkillsDirname)
	}
	return filepath.Join(s.homeDir, ".reasonix", SkillsDirname)
}

// loadBodyWithReferences appends a directory-layout skill's sibling
// references/*.md files to its body (Anthropic Skills compatibility), so depth
// material is available without on-demand resolution. Flat skills have no
// references dir and are returned unchanged.
