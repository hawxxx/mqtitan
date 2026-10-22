package mqtt

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mqtitan/mqtitan/internal/scenario"
)

func keyAt(t *testing.T, dir string, mode os.FileMode) scenario.TLS {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	p := filepath.Join(dir, "client.key")
	if err := os.WriteFile(p, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return scenario.TLS{KeyFile: p}
}

func TestSecretHygieneWideMode(t *testing.T) {
	got := CheckSecretFiles(keyAt(t, t.TempDir(), 0o644))
	if len(got) != 1 || got[0] != (SecretFinding{File: "client.key", Problem: "permissions 0644 are wider than 0600"}) {
		t.Fatalf("got %+v", got)
	}
	for _, m := range []os.FileMode{0o600, 0o400} {
		if got := CheckSecretFiles(keyAt(t, t.TempDir(), m)); len(got) != 0 {
			t.Fatalf("%v: %+v", m, got)
		}
	}
	got = CheckSecretFiles(keyAt(t, t.TempDir(), 0o777))
	if len(got) != 1 || !strings.Contains(got[0].Problem, "the filesystem may not support POSIX modes (for example WSL /mnt/c); move the key to a native filesystem") {
		t.Fatalf("got %+v", got)
	}
}

func TestSecretHygieneGitWorkTree(t *testing.T) {
	for _, mk := range []func(string){
		func(d string) { _ = os.Mkdir(filepath.Join(d, ".git"), 0o700) },
		func(d string) { _ = os.WriteFile(filepath.Join(d, ".git"), []byte("gitdir: x"), 0o600) },
	} {
		root := t.TempDir()
		mk(root)
		sub := filepath.Join(root, "a", "b")
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		got := CheckSecretFiles(keyAt(t, sub, 0o600))
		if len(got) != 1 || !strings.Contains(got[0].Problem, "git work tree") {
			t.Fatalf("got %+v", got)
		}
	}
}

func TestSecretMissingFileIsWarningOnly(t *testing.T) {
	tls := scenario.TLS{KeyFile: filepath.Join(t.TempDir(), "gone.key")}
	got := CheckSecretFiles(tls)
	if len(got) != 1 || got[0].File != "gone.key" || got[0].Problem != "file not found on this host" {
		t.Fatalf("got %+v", got)
	}
	s := scenario.Scenario{APIVersion: "mqtitan.io/v1alpha1", Kind: "Scenario", Name: "n", Broker: scenario.Broker{URL: "mqtts://localhost:8883", Version: "3.1.1", TLS: scenario.TLS{CertFile: "c.pem", KeyFile: tls.KeyFile}},
		Clients: scenario.Clients{Count: 1, IDTemplate: "c"}, Stages: []scenario.Stage{{Duration: scenario.Duration{Duration: 1}, TargetClients: 1}}, Workloads: []scenario.Workload{{Name: "w", Type: "connection", Clients: 1}}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSecretFindingsNeverIncludeFullPath(t *testing.T) {
	dir := t.TempDir()
	tls := keyAt(t, dir, 0o644)
	tls.CertFile = filepath.Join(dir, "missing.crt")
	for _, f := range CheckSecretFiles(tls) {
		if strings.Contains(f.File+f.Problem, dir) {
			t.Fatalf("finding leaks path: %+v", f)
		}
	}
}

func TestTLSConfigStrictSecretsRefuses(t *testing.T) {
	tls := keyAt(t, t.TempDir(), 0o644)
	_, err := TLSConfig(tls, true)
	if err == nil || err.Error() != "refusing key file client.key: permissions 0644 are wider than 0600 (safety.strictSecrets)" {
		t.Fatalf("got %v", err)
	}
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	// The key is not a valid key, so loading fails after the warning.
	_, _ = TLSConfig(tls, false)
	if strings.Count(buf.String(), "level=WARN") != 1 || !strings.Contains(buf.String(), "file=client.key") || strings.Contains(buf.String(), filepath.Dir(tls.KeyFile)) {
		t.Fatalf("log: %s", buf.String())
	}
}

func TestTLSConfigErrorsHaveNoDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := TLSConfig(scenario.TLS{CertFile: filepath.Join(dir, "c.crt"), KeyFile: filepath.Join(dir, "c.key")}, false)
	if err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("got %v", err)
	}
}

func TestStrictRefusesKeyInGitWorkTree(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".git"), 0o700)
	_, err := TLSConfig(keyAt(t, root, 0o600), true)
	if err == nil || !strings.HasPrefix(err.Error(), "refusing key file client.key: file is inside a git work tree") || !strings.HasSuffix(err.Error(), "(safety.strictSecrets)") {
		t.Fatalf("got %v", err)
	}
}
