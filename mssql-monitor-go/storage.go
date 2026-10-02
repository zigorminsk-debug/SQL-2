// storage.go — локальное хранение мониторинга (JSONL-файлы, портабельно).
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Store struct {
	mu  sync.Mutex
	dir string
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

func appendJSONL(path string, v any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

func (s *Store) AppendSample(sm Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return appendJSONL(s.path("samples.jsonl"), sm)
}

func (s *Store) AppendBlocking(evs []BlockingEvent) error {
	if len(evs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return appendJSONL(s.path("blocking.jsonl"), evs)
}

func (s *Store) AppendLong(evs []LongQuery) error {
	if len(evs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return appendJSONL(s.path("long_queries.jsonl"), evs)
}

func readJSONL[T any](path string, t0, t1 int64, useTS func(*T) int64) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	out := []T{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var v T
		if json.Unmarshal(sc.Bytes(), &v) != nil {
			continue
		}
		ts := useTS(&v)
		if t0 != 0 && ts < t0 {
			continue
		}
		if t1 != 0 && ts > t1 {
			continue
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

func (s *Store) LoadSamples(t0, t1 int64) ([]Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readJSONL[Sample](s.path("samples.jsonl"), t0, t1, func(x *Sample) int64 { return x.TS })
}

func (s *Store) LoadBlocking(t0, t1 int64) ([]BlockingEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readJSONL[BlockingEvent](s.path("blocking.jsonl"), t0, t1,
		func(x *BlockingEvent) int64 { return x.TS })
}

func (s *Store) LoadLong(t0, t1 int64) ([]LongQuery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readJSONL[LongQuery](s.path("long_queries.jsonl"), t0, t1,
		func(x *LongQuery) int64 { return x.TS })
}

// SampleCount возвращает (n, min, max) по меткам времени.
func (s *Store) SampleCount() (int, int64, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path("samples.jsonl"))
	if err != nil {
		return 0, 0, 0
	}
	defer f.Close()
	n := 0
	var mn, mx int64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var sm Sample
		if json.Unmarshal(sc.Bytes(), &sm) != nil {
			continue
		}
		if n == 0 || sm.TS < mn {
			mn = sm.TS
		}
		if n == 0 || sm.TS > mx {
			mx = sm.TS
		}
		n++
	}
	return n, mn, mx
}

// Purge удаляет записи старше keepDays дней (перезаписью файлов).
func (s *Store) Purge(keepDays int) {
	cutoff := time.Now().AddDate(0, 0, -keepDays).Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	purgeOne := func(name string, pick func([]byte) int64) {
		p := s.path(name)
		f, err := os.Open(p)
		if err != nil {
			return
		}
		var kept [][]byte
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			if pick(line) >= cutoff {
				kept = append(kept, line)
			}
		}
		f.Close()
		if len(kept) == 0 {
			os.WriteFile(p, nil, 0o644)
			return
		}
		tmp := p + ".tmp"
		os.WriteFile(tmp, nil, 0o644)
		tf, _ := os.OpenFile(tmp, os.O_APPEND|os.O_WRONLY, 0o644)
		for _, l := range kept {
			tf.Write(append(l, '\n'))
		}
		tf.Close()
		os.Rename(tmp, p)
	}
	purgeOne("samples.jsonl", func(b []byte) int64 {
		var v Sample
		json.Unmarshal(b, &v)
		return v.TS
	})
	purgeOne("blocking.jsonl", func(b []byte) int64 {
		var v []BlockingEvent
		json.Unmarshal(b, &v)
		if len(v) > 0 {
			return v[0].TS
		}
		return 0
	})
	purgeOne("long_queries.jsonl", func(b []byte) int64 {
		var v []LongQuery
		json.Unmarshal(b, &v)
		if len(v) > 0 {
			return v[0].TS
		}
		return 0
	})
}
