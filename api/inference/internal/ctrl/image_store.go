package ctrl

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/patrickmn/go-cache"
)

// validateChatKey rejects keys that could escape the store directory or confuse
// the filesystem. Current callers only pass uuid.New().String(), so the check is
// defence in depth: enforcement lives at the filesystem boundary rather than
// relying on every future caller to sanitise first.
func validateChatKey(k string) error {
	if k == "" {
		return fmt.Errorf("chatKey is empty")
	}
	if strings.ContainsAny(k, "/\\\x00") || strings.Contains(k, "..") {
		return fmt.Errorf("chatKey contains forbidden characters")
	}
	return nil
}

// imageStore writes generated image bytes to local disk with a TTL, then cleans
// up automatically via an on-eviction callback.  The chatKey (UUID) doubles as
// the directory name, so only the requester who received ZG-Res-Key can derive
// the serving URL.
type imageStore struct {
	dir   string
	cache *cache.Cache
}

func newImageStore(dir string, ttl time.Duration) (*imageStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create image store dir %q: %w", dir, err)
	}
	s := &imageStore{dir: dir}
	s.cache = cache.New(ttl, ttl/2)
	s.cache.OnEvicted(func(key string, _ interface{}) {
		_ = os.RemoveAll(filepath.Join(dir, key))
	})
	return s, nil
}

// store writes each image to {dir}/{chatKey}/{index}.bin and registers the entry.
func (s *imageStore) store(chatKey string, images [][]byte) error {
	if err := validateChatKey(chatKey); err != nil {
		return err
	}
	keyDir := filepath.Join(s.dir, chatKey)
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		return fmt.Errorf("create image dir: %w", err)
	}
	for i, img := range images {
		path := filepath.Join(keyDir, strconv.Itoa(i)+".bin")
		if err := os.WriteFile(path, img, 0o644); err != nil {
			return fmt.Errorf("write image %d: %w", i, err)
		}
	}
	s.cache.SetDefault(chatKey, len(images))
	return nil
}

// get returns image bytes for the given chatKey and zero-based index.
// Returns an error if the entry has expired or the file is missing.
// Note: OnEvicted runs concurrently with Get; an entry can expire between the
// cache.Get check and the os.ReadFile below, surfacing as ENOENT. Callers
// (handleImageServeRoute) translate any error here to 404, which is the
// correct outcome either way.
func (s *imageStore) get(chatKey string, index int) ([]byte, error) {
	if err := validateChatKey(chatKey); err != nil {
		return nil, err
	}
	if _, ok := s.cache.Get(chatKey); !ok {
		return nil, fmt.Errorf("image not found or expired")
	}
	path := filepath.Join(s.dir, chatKey, strconv.Itoa(index)+".bin")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	return data, nil
}

// Close deletes every cached entry (each Delete fires OnEvicted and removes
// the backing directory) and drops references to the in-memory TTL table.
// The go-cache janitor goroutine is owned by patrickmn/go-cache and stops via
// a runtime finalizer when the *Cache becomes unreachable — we can't signal it
// explicitly, so callers who recreate imageStore frequently (e.g. in tests)
// should let the old *Cache go out of scope and rely on GC to reclaim it.
func (s *imageStore) Close() {
	if s == nil || s.cache == nil {
		return
	}
	for key := range s.cache.Items() {
		s.cache.Delete(key) // triggers OnEvicted → RemoveAll on disk
	}
}

// detectContentType sniffs the MIME type from the first bytes of an image.
func detectContentType(data []byte) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(data)
}
