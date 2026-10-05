package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

var ErrNotFound = errors.New("record not found")

type Store struct {
	db   *sql.DB
	aead cipher.AEAD
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	keyPath := path + ".key"
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		if _, statErr := os.Stat(path); statErr == nil {
			return nil, errors.New("database exists but encryption key is missing; restore its key")
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, createErr := os.OpenFile(keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if createErr != nil {
			return nil, createErr
		}
		_, err = f.Write(key)
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid encryption key length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, aead: aead}
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;
 CREATE TABLE IF NOT EXISTS records (kind TEXT NOT NULL,id TEXT NOT NULL,value BLOB NOT NULL,updated_at INTEGER NOT NULL DEFAULT(unixepoch()),PRIMARY KEY(kind,id));
 CREATE TABLE IF NOT EXISTS samples (test_id TEXT NOT NULL,stamp INTEGER NOT NULL,value BLOB NOT NULL,PRIMARY KEY(test_id,stamp));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) seal(data []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, data, nil), nil
}
func (s *Store) open(data []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(data) < n {
		return nil, errors.New("corrupt encrypted record")
	}
	return s.aead.Open(nil, data[:n], data[n:], nil)
}
func (s *Store) Put(kind, id string, data []byte) error {
	value, err := s.seal(data)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO records(kind,id,value) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET value=excluded.value,updated_at=unixepoch()`, kind, id, value)
	return err
}
func (s *Store) Get(kind, id string) ([]byte, error) {
	var v []byte
	err := s.db.QueryRow(`SELECT value FROM records WHERE kind=? AND id=?`, kind, id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.open(v)
}
func (s *Store) List(kind string, limit int) ([][]byte, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.Query(`SELECT value FROM records WHERE kind=? ORDER BY updated_at DESC,id DESC LIMIT ?`, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([][]byte, 0)
	for rows.Next() {
		var v []byte
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		v, err = s.open(v)
		if err != nil {
			return nil, fmt.Errorf("decrypt record: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) AddSample(id string, stamp int64, data []byte) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO samples(test_id,stamp,value) VALUES(?,?,?)`, id, stamp, data)
	return err
}
func (s *Store) Samples(id string, limit int) ([][]byte, error) {
	if limit <= 0 || limit > 3600 {
		limit = 3600
	}
	rows, err := s.db.Query(`SELECT value FROM (SELECT stamp,value FROM samples WHERE test_id=? ORDER BY stamp DESC LIMIT ?) ORDER BY stamp`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([][]byte, 0)
	for rows.Next() {
		var v []byte
		if err = rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) PruneSamples(id string, before int64) error {
	_, err := s.db.Exec(`DELETE FROM samples WHERE test_id=? AND stamp<?`, id, before)
	return err
}

// CompactSamples preserves cumulative endpoints at progressively coarser resolutions.
// Source buckets are removed in the same transaction, so readers never see holes.
func (s *Store) CompactSamples(id string, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, tier := range []struct{ age, width time.Duration }{{time.Hour, 5 * time.Second}, {24 * time.Hour, 30 * time.Second}, {7 * 24 * time.Hour, time.Minute}} {
		cut := now.Add(-tier.age).UnixNano()
		_, err = tx.Exec(`DELETE FROM samples WHERE test_id=? AND stamp<? AND stamp NOT IN (SELECT MAX(stamp) FROM samples WHERE test_id=? AND stamp<? GROUP BY stamp/?)`, id, cut, id, cut, int64(tier.width))
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DELETE FROM samples WHERE test_id=? AND stamp<?`, id, now.Add(-90*24*time.Hour).UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}
