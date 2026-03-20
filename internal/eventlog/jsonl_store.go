package eventlog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/At0-m/PTrans/internal/domain"
)

type EventStore interface {
	Append(ctx context.Context, evt domain.Event) error
	Replay(ctx context.Context, apply func(domain.Event) error) error
	Close() error
}

type JSONLStore struct {
	mu   sync.Mutex
	file *os.File
	path string
}

func NewJSONLStore(path string) (*JSONLStore, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create event log dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}

	return &JSONLStore{
		file: file,
		path: path,
	}, nil
}

func (s *JSONLStore) Append(ctx context.Context, evt domain.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.file.Write(data); err != nil {
		return fmt.Errorf("write event: %w", err)
	}

	if _, err := s.file.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write newline: %w", err)
	}

	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("sync event log: %w", err)
	}

	return nil
}

func (s *JSONLStore) Replay(ctx context.Context, apply func(domain.Event) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	file, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("open event log for replay: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var evt domain.Event
		if err := json.Unmarshal(line, &evt); err != nil {
			return fmt.Errorf("unmarshal event at line %d: %w", lineNum, err)
		}
		if err := apply(evt); err != nil {
			return fmt.Errorf("apply event at line %d: %w", lineNum, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan event log: %w", err)
	}

	return nil
}

func (s *JSONLStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.file == nil {
		return nil
	}

	err := s.file.Close()
	s.file = nil
	return err
}
