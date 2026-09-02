package utils

import (
	"os"
	"path/filepath"
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

// ResolveDatasetDir returns the directory to read an account's uploaded datasets from:
// DatasetDir, unless the only directory that exists is one written before DatasetDir did
// the folding, in which case that one.
//
// The legacy directory cannot be found BY NAME, and an earlier version of this tried to:
// the spelling it was written under is the one the UPLOAD carried, while the only spelling
// available here is the one the task body carried, and those differing is the entire
// defect. Looking up the caller's own spelling only ever finds the case that already
// worked. So the directory is found by folding what is on disk instead.
//
// The scan is one os.ReadDir per task setup, bounded by the number of accounts that have
// ever uploaded. It exists because the dataset hash is a DURABLE handle — a client can
// upload once and create tasks from it days later — so uploads sitting on disk when the
// folding ships are not a narrow window, and orphaning them would be the same failure
// this function is fixing, pointed the other way. It can go once no unconsumed upload
// predates the change.
//
// Entries are validated with common.IsHexAddress before being folded. HexToAddress is not
// a validator: it truncates and pads, so an unrelated directory name would fold to the
// zero address and could match an account that happens to be it.
func ResolveDatasetDir(userAddress string) string {
	canonical := DatasetDir(userAddress)
	if _, err := os.Stat(canonical); err == nil {
		return canonical
	}
	base := filepath.Join(GetDataDir(), datasetsDirName)
	entries, err := os.ReadDir(base)
	if err != nil {
		// Nothing uploaded yet, or the directory is unreadable. Either way the canonical
		// path is what a "not found" message should name.
		return canonical
	}
	want := canonicalAddressDir(userAddress)
	for _, entry := range entries {
		if !entry.IsDir() || !common.IsHexAddress(entry.Name()) {
			continue
		}
		if canonicalAddressDir(entry.Name()) == want {
			return filepath.Join(base, entry.Name())
		}
	}
	return canonical
}
