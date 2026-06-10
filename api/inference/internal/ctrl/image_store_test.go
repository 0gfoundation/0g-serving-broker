package ctrl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImageStore_StoreAndGet(t *testing.T) {
	store, err := newImageStore(t.TempDir(), time.Minute)
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}

	images := [][]byte{
		[]byte("image-bytes-zero"),
		[]byte("image-bytes-one"),
	}
	chatKey := "test-chat-abc"

	if err := store.store(chatKey, images); err != nil {
		t.Fatalf("store: %v", err)
	}

	for i, want := range images {
		got, err := store.get(chatKey, i)
		if err != nil {
			t.Fatalf("get(%d): %v", i, err)
		}
		if string(got) != string(want) {
			t.Errorf("image[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestImageStore_GetUnknownKey(t *testing.T) {
	store, err := newImageStore(t.TempDir(), time.Minute)
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}
	if _, err := store.get("nonexistent", 0); err == nil {
		t.Error("expected error for unknown key, got nil")
	}
}

func TestImageStore_GetOutOfBoundsIndex(t *testing.T) {
	store, err := newImageStore(t.TempDir(), time.Minute)
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}
	if err := store.store("key", [][]byte{[]byte("only-one")}); err != nil {
		t.Fatalf("store: %v", err)
	}
	// index 1 never written
	if _, err := store.get("key", 1); err == nil {
		t.Error("expected error for out-of-bounds index, got nil")
	}
}

func TestImageStore_GetExpired(t *testing.T) {
	const ttl = 80 * time.Millisecond
	store, err := newImageStore(t.TempDir(), ttl)
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}

	chatKey := "expiring"
	if err := store.store(chatKey, [][]byte{[]byte("img")}); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Wait well past TTL + cleanup interval (ttl/2).
	time.Sleep(ttl * 4)

	if _, err := store.get(chatKey, 0); err == nil {
		t.Error("expected error after TTL expiry, got nil")
	}
}

// TestImageStore_DiskFilesRemovedAfterEviction verifies that when an entry
// expires the backing per-key directory is removed by OnEvicted. We force the
// eviction deterministically by sleeping past TTL and then calling
// DeleteExpired ourselves — relying on the go-cache janitor's ttl/2 tick would
// leave the test racing against scheduler noise.
//
// TTL is set generously (100ms) so the sleep-then-DeleteExpired sequence
// remains robust under loaded CI runners. A previous 10ms value flaked under
// the integration-test job's concurrency, where scheduler quantum delayed
// time.Now() advancement past the cache entry's expiration timestamp.
func TestImageStore_DiskFilesRemovedAfterEviction(t *testing.T) {
	const ttl = 100 * time.Millisecond
	dir := t.TempDir()
	store, err := newImageStore(dir, ttl)
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}

	chatKey := "cleanup-test"
	if err := store.store(chatKey, [][]byte{[]byte("pixel")}); err != nil {
		t.Fatalf("store: %v", err)
	}

	imgPath := filepath.Join(dir, chatKey, "0.bin")
	if _, err := os.Stat(imgPath); err != nil {
		t.Fatalf("image file should exist before TTL: %v", err)
	}

	// Let the entry expire, then fire eviction explicitly — DeleteExpired walks
	// the cache, removes expired items, and calls OnEvicted for each (which
	// runs RemoveAll on the disk directory).
	time.Sleep(ttl * 2)
	store.cache.DeleteExpired()

	if _, err := os.Stat(imgPath); !os.IsNotExist(err) {
		t.Errorf("OnEvicted should have removed %s after TTL; stat err: %v", imgPath, err)
	}
}

// TestImageStore_PurgesOrphansOnInit verifies the disk-growth fix: after a
// broker restart the in-memory TTL table is empty, so leftover per-key
// directories on disk are unreachable via get() and will never fire OnEvicted.
// Without an init-time purge, image_cache/ would grow without bound across
// restarts. The purge is opinionated — any sibling file that's not a directory
// is preserved (so an operator's README or config alongside the store root
// survives).
func TestImageStore_PurgesOrphansOnInit(t *testing.T) {
	dir := t.TempDir()
	// Simulate leftover state from a previous process run.
	for _, key := range []string{"orphan-1", "orphan-2"} {
		keyDir := filepath.Join(dir, key)
		if err := os.MkdirAll(keyDir, 0o755); err != nil {
			t.Fatalf("setup orphan dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(keyDir, "0.bin"), []byte("stale"), 0o644); err != nil {
			t.Fatalf("setup orphan file: %v", err)
		}
	}
	// Non-directory sibling that must NOT be deleted.
	sibling := filepath.Join(dir, "README.md")
	if err := os.WriteFile(sibling, []byte("keep"), 0o644); err != nil {
		t.Fatalf("setup sibling file: %v", err)
	}

	// Opening the store purges the orphan directories.
	if _, err := newImageStore(dir, time.Minute); err != nil {
		t.Fatalf("newImageStore: %v", err)
	}

	for _, key := range []string{"orphan-1", "orphan-2"} {
		if _, err := os.Stat(filepath.Join(dir, key)); !os.IsNotExist(err) {
			t.Errorf("orphan %s should have been removed; stat err: %v", key, err)
		}
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("non-directory sibling %s should be preserved, got: %v", sibling, err)
	}
}

// TestImageStore_UUIDIsTheCapability documents the authz model: the chatKey is
// the ONLY secret needed to retrieve an image (handleImageServeRoute does not
// check session auth — see proxy.go). This mirrors how OpenAI's image URLs
// work: the unguessable token in the path IS the access token. Security
// therefore hinges on callers passing a crypto-random UUID; never a value
// derived from user input or a monotonic counter.
func TestImageStore_UUIDIsTheCapability(t *testing.T) {
	store, err := newImageStore(t.TempDir(), time.Minute)
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}

	if err := store.store("real-secret-uuid", [][]byte{[]byte("private")}); err != nil {
		t.Fatalf("store: %v", err)
	}

	// An attacker guessing a different key — no matter how close — cannot read.
	for _, guess := range []string{"real-secret-uui", "real-secret-uuid2", "other-uuid"} {
		if _, err := store.get(guess, 0); err == nil {
			t.Errorf("get(%q) must fail without exact chatKey, got nil", guess)
		}
	}

	// The exact key retrieves the bytes.
	got, err := store.get("real-secret-uuid", 0)
	if err != nil {
		t.Fatalf("get with correct key: %v", err)
	}
	if string(got) != "private" {
		t.Errorf("get returned wrong bytes: %q", got)
	}
}

// TestImageStore_KeyAllowlist pins the allowlist (^[A-Za-z0-9_-]{1,64}$).
// UUIDs from all current callers match; anything else is rejected — leading
// dots, single ".", colons, control chars, and non-ASCII all surface here
// instead of becoming silent filesystem quirks on some future platform.
func TestImageStore_KeyAllowlist(t *testing.T) {
	store, err := newImageStore(t.TempDir(), time.Minute)
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}

	reject := []string{
		"",                       // empty
		"..",                     // traversal
		"../escape",              // traversal
		"a/../b",                 // traversal
		"foo/bar",                // slash
		`foo\bar`,                // backslash (Windows path sep)
		"nul\x00byte",            // NUL
		".",                      // current-dir alias
		".hidden",                // leading dot (Unix hidden file)
		"trailing.",              // trailing dot (Windows eats this)
		"has:colon",              // reserved on Windows + NTFS ADS
		"with space",             // space
		"tab\there",              // control char
		"新",                     // non-ASCII
		strings.Repeat("a", 65),  // over length cap
	}
	for _, k := range reject {
		k := k
		t.Run("reject:store/"+k, func(t *testing.T) {
			if err := store.store(k, [][]byte{[]byte("x")}); err == nil {
				t.Errorf("store(%q) should reject, got nil", k)
			}
		})
		t.Run("reject:get/"+k, func(t *testing.T) {
			if _, err := store.get(k, 0); err == nil {
				t.Errorf("get(%q) should reject, got nil", k)
			}
		})
	}

	accept := []string{
		"a",                             // single char at lower bound
		"abcDEF-123_xyz",                // mixed charset
		"f47ac10b-58cc-4372-a567-0e02b2c3d479", // canonical UUID
		strings.Repeat("a", 64),         // exactly at length cap
	}
	for _, k := range accept {
		k := k
		t.Run("accept:"+k, func(t *testing.T) {
			if err := store.store(k, [][]byte{[]byte("ok")}); err != nil {
				t.Errorf("store(%q) should succeed, got %v", k, err)
			}
		})
	}
}

// TestImageStore_PartialWriteCleanup pins the leak fix in store(): if any
// os.WriteFile fails mid-batch, SetDefault is never called so OnEvicted would
// never reclaim the half-written keyDir. store() must remove it explicitly.
func TestImageStore_PartialWriteCleanup(t *testing.T) {
	dir := t.TempDir()
	store, err := newImageStore(dir, time.Minute)
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}

	// Pre-create the target of image index 1 AS A DIRECTORY so the write of
	// "1.bin" fails with EISDIR after "0.bin" is already on disk.
	chatKey := "partial-write-test"
	keyDir := filepath.Join(dir, chatKey)
	if err := os.MkdirAll(filepath.Join(keyDir, "1.bin"), 0o755); err != nil {
		t.Fatalf("seed partial-write obstacle: %v", err)
	}

	err = store.store(chatKey, [][]byte{[]byte("zero"), []byte("one")})
	if err == nil {
		t.Fatal("expected store to fail on obstructed index 1")
	}

	if _, statErr := os.Stat(keyDir); !os.IsNotExist(statErr) {
		t.Errorf("keyDir %s must be cleaned up after partial write; stat err: %v", keyDir, statErr)
	}
	// Cache entry must also be absent — get() should fail.
	if _, err := store.get(chatKey, 0); err == nil {
		t.Errorf("cache entry must not be registered after partial write")
	}
}

// TestImageStore_CloseRemovesDiskFiles verifies Close fires OnEvicted for every
// live entry, removing the per-key directory on disk rather than just dropping
// the in-memory TTL table.
func TestImageStore_CloseRemovesDiskFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := newImageStore(dir, time.Minute)
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}
	if err := store.store("k1", [][]byte{[]byte("a")}); err != nil {
		t.Fatalf("store k1: %v", err)
	}
	if err := store.store("k2", [][]byte{[]byte("b"), []byte("c")}); err != nil {
		t.Fatalf("store k2: %v", err)
	}

	store.Close()

	for _, k := range []string{"k1", "k2"} {
		if _, err := os.Stat(dir + "/" + k); !os.IsNotExist(err) {
			t.Errorf("Close did not remove %s (stat err: %v)", k, err)
		}
		if _, err := store.get(k, 0); err == nil {
			t.Errorf("get(%s) after Close should fail, got nil error", k)
		}
	}
}

func TestDetectContentType_PNG(t *testing.T) {
	// Minimal valid PNG header (8 bytes signature).
	pngSig := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 1}
	ct := detectContentType(pngSig)
	if ct != "image/png" {
		t.Errorf("detectContentType(PNG) = %q, want image/png", ct)
	}
}

func TestDetectContentType_JPEG(t *testing.T) {
	// JPEG starts with FF D8 FF.
	jpegSig := make([]byte, 16)
	jpegSig[0] = 0xFF
	jpegSig[1] = 0xD8
	jpegSig[2] = 0xFF
	jpegSig[3] = 0xE0
	ct := detectContentType(jpegSig)
	if ct != "image/jpeg" {
		t.Errorf("detectContentType(JPEG) = %q, want image/jpeg", ct)
	}
}

func TestDetectContentType_Empty(t *testing.T) {
	ct := detectContentType([]byte{})
	if ct != "application/octet-stream" {
		t.Errorf("detectContentType(empty) = %q, want application/octet-stream", ct)
	}
}
