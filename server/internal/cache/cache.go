package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var ErrNotFound = errors.New("segment not found")

type entry struct {
	path    string
	size    int64
	lastUse time.Time
}

// Store is a content-addressed segment cache with LRU eviction by size.
type Store struct {
	dir      string
	maxBytes int64
	mu       sync.Mutex
	entries  map[string]*entry
	total    int64
}

func New(dir string, maxBytes int64) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:      dir,
		maxBytes: maxBytes,
		entries:  make(map[string]*entry),
	}
	_ = s.scan()
	return s, nil
}

func (s *Store) scan() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := e.Name()
		if len(id) != 64 {
			continue
		}
		path := filepath.Join(s.dir, id)
		s.entries[id] = &entry{path: path, size: info.Size(), lastUse: info.ModTime()}
		s.total += info.Size()
	}
	s.evictLocked()
	return nil
}

func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Store) Put(data []byte) (string, error) {
	id := HashBytes(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[id]; ok {
		e.lastUse = time.Now()
		return id, nil
	}
	path := filepath.Join(s.dir, id)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	size := int64(len(data))
	s.entries[id] = &entry{path: path, size: size, lastUse: time.Now()}
	s.total += size
	s.evictLocked()
	return id, nil
}

func (s *Store) Has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if ok {
		e.lastUse = time.Now()
	}
	return ok
}

func (s *Store) Get(id string) ([]byte, error) {
	s.mu.Lock()
	e, ok := s.entries[id]
	if !ok {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	path := e.path
	e.lastUse = time.Now()
	s.mu.Unlock()
	return os.ReadFile(path)
}

func (s *Store) Open(id string) (*os.File, int64, error) {
	s.mu.Lock()
	e, ok := s.entries[id]
	if !ok {
		s.mu.Unlock()
		return nil, 0, ErrNotFound
	}
	path := e.path
	size := e.size
	e.lastUse = time.Now()
	s.mu.Unlock()
	f, err := os.Open(path)
	return f, size, err
}

func (s *Store) PutReader(r io.Reader, maxBytes int64) (string, int64, error) {
	limited := io.LimitReader(r, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", 0, err
	}
	if int64(len(data)) > maxBytes {
		return "", 0, errors.New("segment too large")
	}
	id, err := s.Put(data)
	return id, int64(len(data)), err
}

func (s *Store) Stats() (count int, total int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries), s.total
}

func (s *Store) evictLocked() {
	for s.total > s.maxBytes && len(s.entries) > 0 {
		var oldestID string
		var oldestTime time.Time
		first := true
		for id, e := range s.entries {
			if first || e.lastUse.Before(oldestTime) {
				oldestID = id
				oldestTime = e.lastUse
				first = false
			}
		}
		if oldestID == "" {
			return
		}
		e := s.entries[oldestID]
		_ = os.Remove(e.path)
		s.total -= e.size
		delete(s.entries, oldestID)
	}
}
