package ctrl

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/patrickmn/go-cache"
)

// validChatKey matches the charset all current callers produce (UUIDs via
// uuid.New().String()) and nothing else. An allowlist is safer than a blocklist
// at the filesystem boundary: reject single ".", leading/trailing dots, colons,
// control chars, and every other byte sequence that could cause trouble on
// Windows, NFS, or future code paths. Length cap guards against absurdly long
// paths.
var validChatKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// validateChatKey enforces the allowlist. Callers outside this package must
// continue to produce UUIDs (or compatible IDs) — anything else is a bug we'd
// rather surface here than silently mis-store.
func validateChatKey(k string) error {
	if !validChatKey.MatchString(k) {
		return fmt.Errorf("chatKey %q is not a valid identifier (expected ^[A-Za-z0-9_-]{1,64}$)", k)
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

	// Purge leftover per-key directories from any previous process run.
	// The in-memory TTL table is empty at startup, so those chatKeys are
	// already unreachable via get() — without this sweep the files would
	// accumulate forever across restarts (no OnEvicted ever fires for them).
	// Only remove directories under the store root; leave unexpected loose
	// files alone so an operator doesn't silently lose, e.g., a README.
	// Assumes a single broker process per image_cache directory; sharing
	// the directory between processes would cause one's startup to delete
	// the other's live files.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				_ = os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
	}

	s := &imageStore{dir: dir}
	s.cache = cache.New(ttl, ttl/2)
	s.cache.OnEvicted(func(key string, _ interface{}) {
		_ = os.RemoveAll(filepath.Join(dir, key))
	})
	return s, nil
}

// store writes each image to {dir}/{chatKey}/{index}.bin and registers the entry.
// On any write failure the whole keyDir is removed so a partial-write doesn't
// leave orphan files: SetDefault is never called on error, so OnEvicted would
// never clean them up on its own.
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
			_ = os.RemoveAll(keyDir)
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
