package utils

import (
	"os"
	"path/filepath"
	"testing"
)

// Every spelling the two ingresses accept has to name one directory, or an upload and the
// task that consumes it look in different places.
//
// This is the PR's own scenario one destination further along: the address is authenticated
// through common.HexToAddress at both ends, so all of these are the same account, and each
// could be the one that uploaded while another is the one in the task body.
func TestDatasetDirFoldsEverySpelling(t *testing.T) {
	SetDataDir(t.TempDir())

	const canonical = "0xabcdef1111111111111111111111111111111111"
	want := DatasetDir(canonical)
	if base := filepath.Base(want); base != canonical {
		t.Fatalf("DatasetDir names %q, want the lowercase 0x form %q", base, canonical)
	}

	for _, spelling := range []string{
		"0xABCDEF1111111111111111111111111111111111",
		"0xAbCdEf1111111111111111111111111111111111",
		"abcdef1111111111111111111111111111111111",
		"ABCDEF1111111111111111111111111111111111",
	} {
		if got := DatasetDir(spelling); got != want {
			t.Errorf("DatasetDir(%q) = %q, want %q: an upload under one spelling would be invisible to a task created with another", spelling, got, want)
		}
	}

	// And a different account must not collide, or one user's task would read another's
	// dataset — the opposite failure, and the worse one.
	if other := DatasetDir("0xabcdef1111111111111111111111111111111112"); other == want {
		t.Fatalf("two different accounts share the dataset directory %q", other)
	}
}

// The fallback is only for uploads already on disk when the folding ships. It must find
// them, must prefer the canonical directory once that exists, and must never reach for a
// directory belonging to some other spelling when the canonical one is simply empty.
func TestResolveDatasetDirFindsALegacyUpload(t *testing.T) {
	const eip55 = "0xAbCdEf1111111111111111111111111111111111"

	t.Run("nothing on disk resolves to the canonical directory", func(t *testing.T) {
		SetDataDir(t.TempDir())
		if got, want := ResolveDatasetDir(eip55), DatasetDir(eip55); got != want {
			t.Errorf("ResolveDatasetDir = %q, want %q: a not-found message must name the path a new upload would use", got, want)
		}
	})

	t.Run("a legacy directory is found", func(t *testing.T) {
		root := t.TempDir()
		SetDataDir(root)
		legacy := filepath.Join(root, "datasets", eip55)
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatalf("seed the legacy directory: %v", err)
		}
		if got := ResolveDatasetDir(eip55); got != legacy {
			t.Errorf("ResolveDatasetDir = %q, want the legacy %q: the fix would otherwise orphan an upload it was meant to find", got, legacy)
		}
		// Looked up with the OTHER spelling too, which is the whole point: the task body
		// may carry a different one than the upload did.
		if got := ResolveDatasetDir("0xabcdef1111111111111111111111111111111111"); got != legacy {
			t.Errorf("ResolveDatasetDir(lowercase) = %q, want the legacy %q", got, legacy)
		}
	})

	t.Run("the canonical directory wins once it exists", func(t *testing.T) {
		root := t.TempDir()
		SetDataDir(root)
		legacy := filepath.Join(root, "datasets", eip55)
		canonical := DatasetDir(eip55)
		for _, d := range []string{legacy, canonical} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("seed %s: %v", d, err)
			}
		}
		if got := ResolveDatasetDir(eip55); got != canonical {
			t.Errorf("ResolveDatasetDir = %q, want the canonical %q: with both present the current one has to win, or a stale legacy upload shadows a new one", got, canonical)
		}
	})
}

// An unrelated directory must never be mistaken for an account's.
//
// The scan folds what is on disk, and common.HexToAddress is not a validator — it
// truncates and pads, so "garbage" folds to the zero address. Without the IsHexAddress
// guard, an account that happens to be the zero address would read another directory's
// datasets, and a name shorter than 20 bytes would collide with whatever it pads to.
func TestResolveDatasetDirIgnoresDirectoriesThatAreNotAddresses(t *testing.T) {
	root := t.TempDir()
	SetDataDir(root)
	base := filepath.Join(root, "datasets")
	for _, name := range []string{"garbage", "tmp", "0xnothex", "0x00"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o755); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	// The zero address is the one every short or non-hex name folds to, so it is the
	// account that would collide.
	const zero = "0x0000000000000000000000000000000000000000"
	if got, want := ResolveDatasetDir(zero), DatasetDir(zero); got != want {
		t.Errorf("ResolveDatasetDir(zero address) = %q, want %q: an unrelated directory was folded into an address", got, want)
	}
	// And a plain account is unaffected by the noise.
	const acct = "0xabcdef1111111111111111111111111111111111"
	if got, want := ResolveDatasetDir(acct), DatasetDir(acct); got != want {
		t.Errorf("ResolveDatasetDir = %q, want %q", got, want)
	}
}

// A file, rather than a directory, must not be returned as one.
func TestResolveDatasetDirIgnoresFiles(t *testing.T) {
	root := t.TempDir()
	SetDataDir(root)
	base := filepath.Join(root, "datasets")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	const eip55 = "0xAbCdEf1111111111111111111111111111111111"
	if err := os.WriteFile(filepath.Join(base, eip55), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if got, want := ResolveDatasetDir(eip55), DatasetDir(eip55); got != want {
		t.Errorf("ResolveDatasetDir = %q, want the canonical %q: a file was returned as the dataset directory", got, want)
	}
}
