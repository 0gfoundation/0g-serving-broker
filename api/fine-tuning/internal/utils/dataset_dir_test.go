package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	spellingEIP55 = "0xAbCdEf1111111111111111111111111111111111"
	spellingLower = "0xabcdef1111111111111111111111111111111111"
)

// Dataset hashes have to be what SaveDataset produces — "0x" plus 64 lowercase hex digits —
// because ResolveDatasetPath now refuses anything else. See datasetHashPattern.
var (
	hash1 = "0x" + strings.Repeat("11", 32)
	hash2 = "0x" + strings.Repeat("22", 32)
)

func mustResolve(t *testing.T, userAddress, datasetHash string) string {
	t.Helper()
	got, err := ResolveDatasetPath(userAddress, datasetHash)
	if err != nil {
		t.Fatalf("ResolveDatasetPath(%s, %s): %v", userAddress, datasetHash, err)
	}
	return got
}

// seedDataset writes one uploaded dataset into a directory named exactly as given, which is
// what SaveDataset did before the folding.
func seedDataset(t *testing.T, dirName, datasetHash string) string {
	t.Helper()
	dir := filepath.Join(GetDataDir(), "datasets", dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seed %s: %v", dir, err)
	}
	path := filepath.Join(dir, datasetHash)
	if err := os.WriteFile(path, []byte(`{"text":"x"}`), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	return path
}

// Every spelling the two ingresses accept has to name one directory, or an upload and the
// task that consumes it look in different places.
//
// The address is authenticated through common.HexToAddress at both ends, so all of these
// are the same account, and each could be the one that uploaded while another is the one in
// the task body.
func TestDatasetDirFoldsEverySpelling(t *testing.T) {
	SetDataDir(t.TempDir())

	want := DatasetDir(spellingLower)
	if base := filepath.Base(want); base != spellingLower {
		t.Fatalf("DatasetDir names %q, want the lowercase 0x form %q", base, spellingLower)
	}

	for _, spelling := range []string{
		spellingEIP55,
		"0xABCDEF1111111111111111111111111111111111",
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

// The resolver takes the HASH, and these are the cases that prove why. Resolving the
// DIRECTORY alone was the first version of this, and it broke in both of the first two: a
// directory existing says nothing about whether the dataset being asked for is in it.
func TestResolveDatasetPathResolvesAgainstTheDatasetWanted(t *testing.T) {
	t.Run("an empty canonical directory does not hide a legacy dataset", func(t *testing.T) {
		// SaveDataset creates the canonical directory BEFORE it validates the upload, so
		// any attempt at all — including one rejected for not being JSONL — makes it
		// exist. Resolving the directory alone then returned it for every hash, and every
		// dataset uploaded before the folding became unreachable.
		SetDataDir(t.TempDir())
		legacy := seedDataset(t, spellingEIP55, hash1)
		if err := os.MkdirAll(DatasetDir(spellingLower), 0o755); err != nil {
			t.Fatalf("seed the empty canonical directory: %v", err)
		}
		if got := mustResolve(t, spellingLower, hash1); got != legacy {
			t.Errorf("ResolveDatasetPath = %q, want %q: an empty canonical directory shadowed the dataset", got, legacy)
		}
	})

	t.Run("two legacy directories are each found for what they hold", func(t *testing.T) {
		// And this one needs no new upload at all. The premise of the folding is that a
		// client uploads under wallet.address one day and signer.address.toLowerCase() the
		// next — so such an account already has two legacy directories, and one of them IS
		// the canonical name. Resolving the directory alone picked that one and lost the
		// other entirely.
		SetDataDir(t.TempDir())
		inEIP55 := seedDataset(t, spellingEIP55, hash1)
		inLower := seedDataset(t, spellingLower, hash2)

		for _, tc := range []struct {
			hash string
			want string
		}{
			{hash1, inEIP55},
			{hash2, inLower},
		} {
			// Asked with each spelling, since either can be the one in the task body.
			for _, asked := range []string{spellingEIP55, spellingLower} {
				if got := mustResolve(t, asked, tc.hash); got != tc.want {
					t.Errorf("ResolveDatasetPath(%s, %s) = %q, want %q", asked, tc.hash, got, tc.want)
				}
			}
		}
	})

	t.Run("a folded directory that does not hold the hash is skipped", func(t *testing.T) {
		// The scan can meet several directories that fold to the same account, and the
		// first one it meets need not be the one holding the dataset. Written with two
		// spellings whose ASCII order puts the EMPTY one first — os.ReadDir sorts, and
		// "0xABCDEF…" precedes "0xAbCdEf…" — so a scan that returned its first folded
		// match rather than its first HOLDING match returns a path that does not exist.
		//
		// The first version of this file missed that: its two-directory case happened to
		// have the dataset in whichever directory sorted first, so it passed either way.
		SetDataDir(t.TempDir())
		empty := filepath.Join(GetDataDir(), "datasets", "0xABCDEF1111111111111111111111111111111111")
		if err := os.MkdirAll(empty, 0o755); err != nil {
			t.Fatalf("seed the empty directory: %v", err)
		}
		want := seedDataset(t, spellingEIP55, hash1)
		if got := mustResolve(t, spellingLower, hash1); got != want {
			t.Errorf("ResolveDatasetPath = %q, want %q: the scan stopped at a directory that does not hold the dataset", got, want)
		}
	})

	t.Run("the pre-converted HF form counts as present", func(t *testing.T) {
		// Setup prefers {hash}_hf and falls back to {hash}. A directory holding only the
		// converted form still holds the dataset, and the returned path is the base one so
		// setup's own two-step lookup keeps working.
		SetDataDir(t.TempDir())
		seedDataset(t, spellingEIP55, hash1+"_hf")
		want := filepath.Join(GetDataDir(), "datasets", spellingEIP55, hash1)
		if got := mustResolve(t, spellingLower, hash1); got != want {
			t.Errorf("ResolveDatasetPath = %q, want the base path %q", got, want)
		}
	})

	t.Run("a dataset in the canonical directory is preferred", func(t *testing.T) {
		SetDataDir(t.TempDir())
		seedDataset(t, spellingEIP55, hash1)
		inCanonical := seedDataset(t, filepath.Base(DatasetDir(spellingLower)), hash1)
		if got := mustResolve(t, spellingEIP55, hash1); got != inCanonical {
			t.Errorf("ResolveDatasetPath = %q, want the canonical %q: with the hash in both, the current directory has to win", got, inCanonical)
		}
	})

	t.Run("nothing anywhere names the canonical path", func(t *testing.T) {
		SetDataDir(t.TempDir())
		want := filepath.Join(DatasetDir(spellingEIP55), hash1)
		if got := mustResolve(t, spellingEIP55, hash1); got != want {
			t.Errorf("ResolveDatasetPath = %q, want %q: a not-found message must name where a new upload would go", got, want)
		}
		// Including when other accounts have uploaded, so the scan cannot wander.
		seedDataset(t, "0x1111111111111111111111111111111111111111", hash1)
		if got := mustResolve(t, spellingEIP55, hash1); got != want {
			t.Errorf("ResolveDatasetPath = %q, want %q: another account's dataset was returned", got, want)
		}
	})
}

// An unrelated directory must never be folded into an address.
//
// common.HexToAddress is not a validator — it truncates and pads, so "garbage" folds to the
// zero address. Without the IsHexAddress guard the account that happens to be the zero
// address would read another directory's datasets.
func TestResolveDatasetPathIgnoresDirectoriesThatAreNotAddresses(t *testing.T) {
	SetDataDir(t.TempDir())
	const zero = "0x0000000000000000000000000000000000000000"
	for _, name := range []string{"garbage", "tmp", "0xnothex", "0x00"} {
		seedDataset(t, name, hash1)
	}
	if got, want := mustResolve(t, zero, hash1), filepath.Join(DatasetDir(zero), hash1); got != want {
		t.Errorf("ResolveDatasetPath = %q, want %q: an unrelated directory was folded into an address", got, want)
	}
}

// A file where a directory is expected must not be returned as one.
//
// The outcome is asserted, not the mechanism: holdsDataset already makes a non-directory
// unusable (stat of a path under a file fails), so removing the IsDir guard from the scan
// leaves this passing. The guard stays because "only directories are candidates" is what
// the loop means, not because this test would catch its removal.
func TestResolveDatasetPathIgnoresFiles(t *testing.T) {
	root := t.TempDir()
	SetDataDir(root)
	base := filepath.Join(root, "datasets")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, spellingEIP55), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if got, want := mustResolve(t, spellingEIP55, hash1), filepath.Join(DatasetDir(spellingEIP55), hash1); got != want {
		t.Errorf("ResolveDatasetPath = %q, want the canonical %q", got, want)
	}
}
