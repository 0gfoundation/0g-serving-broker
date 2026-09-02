package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The dataset hash is a path element, and until this guard it was the only one that was
// never validated.
//
// DatasetDir's doc explains at length why UserAddress must pass common.IsHexAddress before
// it can be a directory name. The hash sits right beside it in the same filepath.Join and
// had no check at all: schema.Task.Bind validates UserAddress and copies DatasetHash
// through. filepath.Join cleans "..", so a hash of "../0xVICTIM/0x<their hash>" resolved
// under another account's directory, holdsDataset found it, and setup's useLocalDataset
// symlinked it as the training dataset — no containment check there either. The attacker
// signs only their own fields, so the signature is no barrier, and prepareData runs before
// verifySignature regardless.
//
// The result was a LoRA trained on someone else's uploaded data.
func TestResolveDatasetPathRefusesAHashThatIsNotABareToken(t *testing.T) {
	root := t.TempDir()
	SetDataDir(root)

	const attacker = "0x1111111111111111111111111111111111111111"
	const victim = "0x2222222222222222222222222222222222222222"
	victimHash := "0x" + strings.Repeat("ab", 32)

	// The victim's uploaded dataset, in their own folded directory.
	victimDir := DatasetDir(victim)
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatalf("seed the victim directory: %v", err)
	}
	victimPath := filepath.Join(victimDir, victimHash)
	if err := os.WriteFile(victimPath, []byte(`{"text":"private"}`), 0o600); err != nil {
		t.Fatalf("seed the victim dataset: %v", err)
	}

	// Every shape that reached out of the attacker's own directory, plus the ones that
	// merely are not a dataset name. None may resolve to a path outside
	// datasets/<attacker>/, and none may resolve to something that exists.
	for _, hash := range []string{
		"../" + strings.ToLower(victim) + "/" + victimHash,
		"../../datasets/" + strings.ToLower(victim) + "/" + victimHash,
		strings.ToLower(victim) + "/" + victimHash, // no "..", still a second element
		"..",
		".",
		"/etc/passwd",
		"",
		"0x" + strings.Repeat("ab", 31),        // 62 hex digits: too short
		"0x" + strings.Repeat("ab", 33),        // 66: too long
		strings.Repeat("ab", 32),               // no 0x prefix
		"0X" + strings.Repeat("ab", 32),        // uppercase prefix
		"0x" + strings.Repeat("AB", 32),        // uppercase digits
		"0x" + strings.Repeat("ab", 31) + "zz", // not hex
	} {
		t.Run(hash, func(t *testing.T) {
			got, err := ResolveDatasetPath(attacker, hash)
			if err != nil {
				// Refused outright, which is the intended answer for anything that is not
				// a dataset name. Nothing is joined, so there is nothing left to check.
				return
			}

			// It must never name the victim's file.
			if got == victimPath {
				t.Fatalf("ResolveDatasetPath returned the victim's dataset %q", got)
			}
			// Nor anything else that exists — the whole point of the traversal is that
			// setup then symlinks whatever came back.
			if _, err := os.Stat(got); err == nil {
				t.Fatalf("ResolveDatasetPath returned an existing path %q for a hash that is not a dataset name", got)
			}
			// And it must stay inside the caller's own folded directory, so no future
			// caller can be surprised by where the answer points.
			ownDir := DatasetDir(attacker)
			if rel, err := filepath.Rel(ownDir, got); err != nil || strings.HasPrefix(rel, "..") {
				t.Errorf("ResolveDatasetPath = %q, which is outside %q (rel %q, err %v)", got, ownDir, rel, err)
			}
		})
	}

	// And the guard must not break the real thing: a well-formed hash still resolves.
	if err := os.MkdirAll(DatasetDir(attacker), 0o755); err != nil {
		t.Fatalf("seed the attacker directory: %v", err)
	}
	ownHash := "0x" + strings.Repeat("cd", 32)
	ownPath := filepath.Join(DatasetDir(attacker), ownHash)
	if err := os.WriteFile(ownPath, []byte(`{"text":"mine"}`), 0o600); err != nil {
		t.Fatalf("seed the attacker dataset: %v", err)
	}
	if got := mustResolve(t, attacker, ownHash); got != ownPath {
		t.Errorf("ResolveDatasetPath = %q, want %q: the guard rejected a valid dataset hash", got, ownPath)
	}
}

// The pattern's exact length is not what closes the traversal — the anchors are — so it is
// asserted separately: a hash of the wrong length must be REFUSED, not merely resolved to
// something that does not exist.
//
// It matters because this resolver can only ever find what SaveDataset wrote, which is "0x"
// plus 64 lowercase hex digits. Anything else names a file nothing writes, and answering
// with a path for it would be inventing a second spelling of a dataset name — the same
// two-spellings defect the address folding exists to fix. A clear refusal also tells the
// caller which source to stop looking in, instead of a confusing "not found".
func TestResolveDatasetPathRefusesAWrongLengthHash(t *testing.T) {
	SetDataDir(t.TempDir())
	const account = "0x1111111111111111111111111111111111111111"
	for _, hash := range []string{
		"0x",
		"0x" + strings.Repeat("a", 1),
		"0x" + strings.Repeat("a", 63),
		"0x" + strings.Repeat("a", 65),
		"0x" + strings.Repeat("a", 128),
	} {
		if got, err := ResolveDatasetPath(account, hash); err == nil {
			t.Errorf("ResolveDatasetPath(%q) = %q with no error, want a refusal: only what SaveDataset writes can be found here", hash, got)
		}
	}
	// And the exact length still passes, so the bound is not off by one.
	if _, err := ResolveDatasetPath(account, "0x"+strings.Repeat("a", 64)); err != nil {
		t.Errorf("a 64-digit hash was refused: %v", err)
	}
}
