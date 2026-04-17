package ctrl

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/patrickmn/go-cache"
)

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
func (s *imageStore) get(chatKey string, index int) ([]byte, error) {
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

// detectContentType sniffs the MIME type from the first bytes of an image.
func detectContentType(data []byte) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(data)
}
