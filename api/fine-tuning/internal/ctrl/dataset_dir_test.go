package ctrl

import (
	"bytes"
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
)

// testCtrl is the smallest Ctrl SaveDataset needs: no db, no contract and no TEE service,
// only the data dir, a logger, and a config — SaveDataset ends by trying to convert the
// JSONL to HF format, which reads Images.ExecutionImageName. That conversion is expected to
// fail here (there is no python3 environment or execution image) and its error is swallowed
// by design, since the setup service converts too; what this test is about is where the
// file lands, which is decided before it.
func testCtrl(t *testing.T) *Ctrl {
	t.Helper()
	cfg := config.GetConfig()
	logger, err := log.GetLogger(&cfg.Logger)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	// An image reference the docker CLI rejects before contacting a registry, so the
	// conversion's docker fallback fails in milliseconds instead of pulling. Without this
	// the two SaveDataset calls below spent about twelve seconds between them on work this
	// test does not assert anything about.
	cfg.Images.ExecutionImageName = "invalid reference"
	return &Ctrl{logger: logger, config: cfg}
}

// uploadedFile builds the *multipart.FileHeader the upload handler hands SaveDataset.
func uploadedFile(t *testing.T, name, content string) *multipart.FileHeader {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	form, err := multipart.NewReader(&buf, w.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("read form: %v", err)
	}
	t.Cleanup(func() { _ = form.RemoveAll() })
	return form.File["file"][0]
}

// The two ends of the dataset path have to agree, and this is the test that was missing
// when they did not.
//
// SaveDataset names the directory from the URL path parameter; task setup reads it back
// from schema.Task.UserAddress, which came from the JSON body. Both ingresses accept every
// spelling common.IsHexAddress does and VerifyUploadSignature authenticates all of them, so
// the two strings are routinely different for the same account — upload with
// wallet.address, create the task with signer.address.toLowerCase(). On a case-sensitive
// filesystem the lookup then missed a dataset sitting right there, setup fell through to 0G
// Storage (which holds nothing for a TEE-only upload), and the task failed.
//
// Neither end was ever tested against the other, which is how the pair could disagree at
// all. This asserts the crossing directly: written under one spelling, found under a
// different one.
func TestSaveDatasetIsFoundUnderAnotherSpellingOfTheSameAddress(t *testing.T) {
	utils.SetDataDir(t.TempDir())

	const upload = "0xAbCdEf1111111111111111111111111111111111" // EIP-55, e.g. wallet.address
	const lookup = "0xabcdef1111111111111111111111111111111111" // lower, e.g. signer.address.toLowerCase()

	c := testCtrl(t)
	hash, err := c.SaveDataset(upload, uploadedFile(t, "train.jsonl", `{"text":"hello"}`))
	if err != nil {
		t.Fatalf("SaveDataset: %v", err)
	}
	if hash == "" {
		t.Fatal("SaveDataset returned no dataset hash")
	}

	// Exactly the expression services/setup.go uses to find an uploaded dataset.
	found, err := utils.ResolveDatasetPath(lookup, hash)
	if err != nil {
		t.Fatalf("ResolveDatasetPath: %v", err)
	}
	if _, err := os.Stat(found); err != nil {
		t.Fatalf("a dataset uploaded as %s is not visible to a task created as %s: %v\nlooked at %s", upload, lookup, err, found)
	}

	// And the reverse crossing, since either spelling can be on either end.
	c2 := testCtrl(t)
	hash2, err := c2.SaveDataset(lookup, uploadedFile(t, "train2.jsonl", `{"text":"world"}`))
	if err != nil {
		t.Fatalf("SaveDataset (lower): %v", err)
	}
	back, err := utils.ResolveDatasetPath(upload, hash2)
	if err != nil {
		t.Fatalf("ResolveDatasetPath: %v", err)
	}
	if _, err := os.Stat(back); err != nil {
		t.Fatalf("a dataset uploaded as %s is not visible to a task created as %s: %v", lookup, upload, err)
	}

	// Both landed in ONE directory, which is the property that makes the crossing work
	// rather than a fallback happening to paper over it.
	if got, want := filepath.Dir(found), utils.DatasetDir(upload); got != want {
		t.Errorf("dataset directory = %q, want the folded %q", got, want)
	}
	entries, err := os.ReadDir(filepath.Join(utils.GetDataDir(), "datasets"))
	if err != nil {
		t.Fatalf("read datasets dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("datasets/ holds %d directories %q, want 1: two spellings of one account each got their own", len(entries), names)
	}
}
