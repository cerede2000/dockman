package files

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const editorLeaseTTL = 90 * time.Second

type FileChange struct {
	Host    string `json:"host"`
	Path    string `json:"path"`
	Session string `json:"session,omitempty"`
}

type editorLease struct {
	session string
	expires time.Time
}

type editorState struct {
	mu          sync.Mutex
	leases      map[string]editorLease
	subscribers map[chan FileChange]struct{}
}

func newEditorState() *editorState {
	return &editorState{leases: map[string]editorLease{}, subscribers: map[chan FileChange]struct{}{}}
}

func editorKey(host, path string) string {
	return strings.TrimSpace(host) + "\x00" + filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(path))))
}

func fileRevision(contents []byte) string {
	revision, _ := hashRevision(strings.NewReader(string(contents)))
	return revision
}

func hashRevision(reader io.Reader) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *editorState) setLease(host, path, session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	s.leases[editorKey(host, path)] = editorLease{session: session, expires: time.Now().Add(editorLeaseTTL)}
}

func (s *editorState) releaseLease(host, path, session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := editorKey(host, path)
	if lease, ok := s.leases[key]; ok && lease.session == session {
		delete(s.leases, key)
	}
}

func (s *editorState) dirtyPaths(host string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	prefix := strings.TrimSpace(host) + "\x00"
	paths := make([]string, 0)
	for key := range s.leases {
		if strings.HasPrefix(key, prefix) {
			paths = append(paths, strings.TrimPrefix(key, prefix))
		}
	}
	return paths
}

func (s *editorState) pruneLocked(now time.Time) {
	for key, lease := range s.leases {
		if !lease.expires.After(now) {
			delete(s.leases, key)
		}
	}
}

func (s *editorState) subscribe() (chan FileChange, func()) {
	ch := make(chan FileChange, 8)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if _, ok := s.subscribers[ch]; ok {
			delete(s.subscribers, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
}

func (s *editorState) publish(change FileChange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subscribers {
		select {
		case ch <- change:
		default:
			// A slow browser will reload the newest file state; intermediate
			// notifications carry no useful state and may safely be coalesced.
		}
	}
}
