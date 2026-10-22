package mqtt

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mqtitan/mqtitan/internal/scenario"
)

// SecretFinding describes a risk in a TLS file. File is the basename only: full
// paths and key contents never appear in findings or logs.
type SecretFinding struct {
	File    string
	Problem string
}

// CheckSecretFiles inspects the configured caFile, certFile and keyFile
// paths. Missing files are findings (the file may live on another host).
// Inline PEM is not checked.
func CheckSecretFiles(t scenario.TLS) []SecretFinding {
	out := checkSecretFile(t.KeyFile, true)
	out = append(out, checkSecretFile(t.CertFile, false)...)
	return append(out, checkSecretFile(t.CAFile, false)...)
}

func checkSecretFile(path string, key bool) []SecretFinding {
	if path == "" {
		return nil
	}
	base := filepath.Base(path)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return []SecretFinding{{base, "file not found on this host"}}
	}
	if err != nil {
		return []SecretFinding{{base, "file cannot be inspected"}}
	}
	var out []SecretFinding
	if mode := info.Mode().Perm(); key && runtime.GOOS != "windows" && mode&0o077 != 0 {
		problem := fmt.Sprintf("permissions %04o are wider than 0600", mode)
		if mode == 0o777 {
			problem += "; the filesystem may not support POSIX modes (for example WSL /mnt/c); move the key to a native filesystem"
		}
		out = append(out, SecretFinding{base, problem})
	}
	if inGitWorkTree(path) {
		out = append(out, SecretFinding{base, "file is inside a git work tree; it could be committed by accident"})
	}
	return out
}

func inGitWorkTree(path string) bool {
	dir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return false
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// checkKeyFile enforces or logs key file findings. With strict set, a finding
// refuses the key; missing files are left to the loader's own error.
func checkKeyFile(path string, strict bool) error {
	for _, f := range checkSecretFile(path, true) {
		if f.Problem == "file not found on this host" {
			continue
		}
		if strict {
			return fmt.Errorf("refusing key file %s: %s (safety.strictSecrets)", f.File, f.Problem)
		}
		slog.Warn("TLS key file risk", "file", f.File, "problem", f.Problem)
	}
	return nil
}
