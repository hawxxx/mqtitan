package storage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistEncryptedRecordsAndRecover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(`{"password":"never-store-me","status":"running"}`)
	if err = s.Put("tests", "run-one", secret); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + "-wal"} {
		b, _ := os.ReadFile(name)
		if bytes.Contains(b, []byte("never-store-me")) {
			t.Fatal("plaintext secret in database")
		}
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Get("tests", "run-one")
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("reopen: %s %v", got, err)
	}
	info, err := os.Stat(path + ".key")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key permissions: %v %v", info, err)
	}
}

func TestCompactionRetainsOldHistoryWithoutDuplicateBuckets(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1000000000, 0)
	old := now.Add(-2 * time.Hour).Truncate(5 * time.Second)
	for i := int64(0); i < 10; i++ {
		if err = s.AddSample("a", old.UnixNano()+i*int64(time.Second), []byte(fmt.Sprintf("%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.CompactSamples("a", now); err != nil {
		t.Fatal(err)
	}
	got, err := s.Samples("a", 100)
	if err != nil || len(got) != 2 || string(got[0]) != "4" || string(got[1]) != "9" {
		t.Fatalf("history lost or not compacted: %q %v", got, err)
	}
}

func TestBoundedMetricHistory(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := int64(1); i <= 5; i++ {
		if err := s.AddSample("a", i, []byte(`{"connected":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	samples, err := s.Samples("a", 2)
	if err != nil || len(samples) != 2 {
		t.Fatalf("bounded samples: %v %v", samples, err)
	}
}
