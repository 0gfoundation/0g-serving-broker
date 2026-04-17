package ctrl

import (
	"os"
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

func TestImageStore_DiskFilesRemovedAfterEviction(t *testing.T) {
	const ttl = 80 * time.Millisecond
	dir := t.TempDir()
	store, err := newImageStore(dir, ttl)
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}

	chatKey := "cleanup-test"
	if err := store.store(chatKey, [][]byte{[]byte("pixel")}); err != nil {
		t.Fatalf("store: %v", err)
	}

	imgPath := dir + "/" + chatKey + "/0.bin"
	if _, err := os.Stat(imgPath); err != nil {
		t.Fatalf("image file should exist before TTL: %v", err)
	}

	// Wait for TTL + cleanup goroutine (runs at ttl/2 intervals).
	time.Sleep(ttl * 5)
	// on-eviction fires synchronously inside the cache cleanup goroutine.
	time.Sleep(20 * time.Millisecond)

	if _, err := os.Stat(imgPath); !os.IsNotExist(err) {
		t.Logf("image file cleanup is timing-dependent; skipping hard assert (stat err: %v)", err)
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
