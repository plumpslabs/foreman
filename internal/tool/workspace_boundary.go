package tool

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// checkWorkspacePathArgs checks common path-bearing tool arguments before a
// tool sees them. Existing paths are symlink-resolved; new paths are checked
// using their nearest existing parent to prevent escaping through symlinks.
func checkWorkspacePathArgs(root, raw string) error {
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil
	}
	for _, key := range []string{"path", "file", "directory", "dir", "root", "target"} {
		var value string
		if json.Unmarshal(args[key], &value) != nil || strings.TrimSpace(value) == "" {
			continue
		}
		if err := ensureWorkspacePath(root, value); err != nil {
			return err
		}
	}
	for _, key := range []string{"paths", "files"} {
		var values []string
		if json.Unmarshal(args[key], &values) != nil {
			continue
		}
		for _, value := range values {
			if err := ensureWorkspacePath(root, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureWorkspacePath(root, raw string) error {
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	resolved := abs
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		resolved = real
	} else {
		parent := abs
		var missing []string
		for {
			if realParent, e := filepath.EvalSymlinks(parent); e == nil {
				for i := len(missing) - 1; i >= 0; i-- {
					realParent = filepath.Join(realParent, missing[i])
				}
				resolved = realParent
				break
			}
			next := filepath.Dir(parent)
			if next == parent {
				return fmt.Errorf("cannot safely resolve path %q", raw)
			}
			missing = append(missing, filepath.Base(parent))
			parent = next
		}
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path %q is outside the active workspace", raw)
	}
	// Compare the deepest existing ancestor through its real path. This rejects
	// existing symlink directories that point outside the workspace, including
	// paths whose final component does not yet exist.
	return nil
}
