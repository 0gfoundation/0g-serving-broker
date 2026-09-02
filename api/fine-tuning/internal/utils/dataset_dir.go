package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// datasetsDirName is the subdirectory of the data dir that UploadDataset writes into.
const datasetsDirName = "datasets"

// DatasetDir is the one directory an account's uploaded datasets live in.
//
// The address is folded to a single spelling, because the two ends of this path come from
// different places and neither normalises. UploadDataset takes it from the URL path
// parameter and SaveDataset uses that string as the directory name; task setup looks the
// directory up from schema.Task.UserAddress, which came from the JSON body. Both ingresses
// accept every spelling common.IsHexAddress does — lower, EIP-55, upper, and bare with no
// "0x" — and VerifyUploadSignature compares through common.HexToAddress, so every one of
// them authenticates.
//
// So: upload with wallet.address and create the task with signer.address.toLowerCase(), and
// on a case-sensitive filesystem the lookup misses a dataset sitting right there. Setup
// falls through to 0G Storage, which holds nothing for a TEE-only upload, and the task
// fails — with a log line saying the dataset was not found, which is true of the path it
// looked at and false of the disk. It works on a case-insensitive filesystem, which is how
// it survives a developer's laptop and fails on ext4.
//
// # The caller must have validated the address first
//
// common.HexToAddress is NOT a validator: hex.DecodeString returns its partial output, so
// "0x" + <40 hex> + "/../.." decodes to exactly those 20 bytes and would fold here to a
// clean-looking directory while the original string was a traversal. common.IsHexAddress
// is what rejects it, and schema.Task.Bind and SaveDataset both run it before anything
// reaches here. This function normalises; it does not validate.
func DatasetDir(userAddress string) string {
	return filepath.Join(GetDataDir(), datasetsDirName, canonicalAddressDir(userAddress))
}

// canonicalAddressDir folds every accepted spelling of an address onto one directory name:
// "0x" plus 40 lowercase hex digits. Lowercased rather than left in EIP-55 form so the name
// carries no case at all, which is one less thing for a case-insensitive filesystem to make
// look correct.
func canonicalAddressDir(userAddress string) string {
	return strings.ToLower(common.HexToAddress(userAddress).Hex())
}

// datasetHashPattern is what a dataset hash may be: exactly what SaveDataset produces,
// which is "0x" plus the lowercase hex of a Keccak-256 (crypto.NewKeccakState, not sha256
// — the digest is 32 bytes either way, so the pattern is the same, but saying the wrong
// one sends the next reader to the wrong writer).
//
// It exists because the hash is a PATH ELEMENT and was the only one never checked.
// DatasetDir's doc above explains at length why UserAddress has to pass
// common.IsHexAddress before it can be a directory name; the hash sits beside it in the
// same filepath.Join and had nothing — schema.Task.Bind validates UserAddress and copies
// DatasetHash through untouched. filepath.Join cleans "..", so a hash of
// "../0xVICTIM/0x<their hash>" resolved under another account's directory, holdsDataset
// found it, and setup's useLocalDataset symlinked it as the training dataset with no
// containment check of its own. The attacker signs only their own fields, so the signature
// is no barrier, and prepareData runs before verifySignature regardless. The result was a
// LoRA trained on someone else's uploaded data.
//
// Deliberately exact rather than "no separators": this resolver can only ever find what
// SaveDataset wrote, so accepting a second spelling — uppercase digits, a "0X" prefix —
// would invent a hash that names a file nothing writes, which is the same two-spellings
// defect the address folding above exists to fix.
//
// A hash that does not match is not necessarily hostile: a 0G Storage root hash spelled
// some other way reaches here too. That case is unaffected in the end — the caller records
// the refusal as an uploaded-source error and the 0G Storage step still runs.
var datasetHashPattern = regexp.MustCompile(`^0x[0-9a-f]{64}$`)

// hfDatasetSuffix marks the pre-converted HuggingFace form of an uploaded dataset, which
// sits beside the raw JSONL under the same hash. Task setup prefers it; both count as the
// dataset being present.
const hfDatasetSuffix = "_hf"

// ResolveDatasetPath returns the base path of one uploaded dataset: DatasetDir's folded
// directory joined with the hash, unless the dataset is actually sitting in a directory
// written before the folding, in which case that one.
//
// It takes the HASH, and that is the whole point. The first version of this resolved the
// DIRECTORY alone — canonical if it existed, else a scan — and that is broken in two
// ordinary ways, because a directory existing says nothing about whether the dataset being
// asked for is in it:
//
//   - SaveDataset creates the canonical directory before it validates the upload, so any
//     attempt at all — including one rejected for not being JSONL — makes the canonical
//     directory exist. From then on every legacy dataset for that account is unreachable.
//   - Worse, no new upload is needed. The premise of the folding is that a client uploads
//     under wallet.address one day and signer.address.toLowerCase() the next, so such an
//     account ALREADY has two legacy directories — and one of them is the lowercase name,
//     which is the canonical one. It wins, and everything in the other is lost.
//
// Resolving against the dataset that is wanted has neither problem: an empty canonical
// directory does not match, and two legacy directories are each found for the hashes they
// hold. A directory counts as holding the dataset if either the raw file or its _hf form is
// there, matching what setup then looks for.
//
// The scan cannot be replaced by a lookup: the spelling a legacy directory was written
// under is the one the UPLOAD carried, while the only spelling available here is the one
// the task body carried, and those differing is the entire defect. It is one os.ReadDir per
// task setup, bounded by the accounts that have ever uploaded, and it exists because the
// dataset hash is a durable handle — a client can upload once and create tasks from it days
// later — so orphaning what is already on disk would be this same failure pointed the other
// way. It can go once no unconsumed upload predates the folding.
//
// Entries are checked with common.IsHexAddress before being folded, because HexToAddress
// truncates and pads: an unrelated directory name would fold to the zero address.
func ResolveDatasetPath(userAddress, datasetHash string) (string, error) {
	// Before anything is joined. An error rather than a best-effort path, because a
	// function that returns a path it is not sure about is the shape that produced the
	// traversal: every caller then has to remember to re-check what it got back.
	if !datasetHashPattern.MatchString(datasetHash) {
		return "", fmt.Errorf("dataset hash %q is not a dataset name; it must be \"0x\" and 64 lowercase hex digits", datasetHash)
	}
	canonical := DatasetDir(userAddress)
	if holdsDataset(canonical, datasetHash) {
		return filepath.Join(canonical, datasetHash), nil
	}
	base := filepath.Join(GetDataDir(), datasetsDirName)
	entries, err := os.ReadDir(base)
	if err != nil {
		// Nothing uploaded yet, or the directory is unreadable. Either way the canonical
		// path is what a "not found" message should name.
		return filepath.Join(canonical, datasetHash), nil
	}
	want := canonicalAddressDir(userAddress)
	for _, entry := range entries {
		if !entry.IsDir() || !common.IsHexAddress(entry.Name()) {
			continue
		}
		if canonicalAddressDir(entry.Name()) != want {
			continue
		}
		if dir := filepath.Join(base, entry.Name()); holdsDataset(dir, datasetHash) {
			return filepath.Join(dir, datasetHash), nil
		}
	}
	// Not found anywhere. The canonical path is returned so the caller's message names
	// where a new upload would have put it, rather than a legacy spelling nothing writes.
	return filepath.Join(canonical, datasetHash), nil
}

// holdsDataset says whether a directory contains the named dataset in either of the two
// forms task setup accepts.
func holdsDataset(dir, datasetHash string) bool {
	path := filepath.Join(dir, datasetHash)
	if _, err := os.Stat(path + hfDatasetSuffix); err == nil {
		return true
	}
	_, err := os.Stat(path)
	return err == nil
}
