//go:build integration

package db

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/0glabs/0g-serving-broker/inference/model"
)

func migrateVideoJobOwnerTable(t *testing.T, d *DB) {
	t.Helper()
	if err := d.db.AutoMigrate(&model.VideoJobOwner{}); err != nil {
		t.Fatalf("auto-migrate video_job_owner table: %v", err)
	}
}

func TestCreateAndGetVideoJobOwner(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoJobOwnerTable(t, d)

	if err := d.CreateVideoJobOwner("job-1", "0xUserA"); err != nil {
		t.Fatalf("CreateVideoJobOwner: %v", err)
	}

	owner, err := d.GetVideoJobOwner("job-1")
	if err != nil {
		t.Fatalf("GetVideoJobOwner: %v", err)
	}
	if owner.ProviderJobID != "job-1" {
		t.Errorf("ProviderJobID = %q, want job-1", owner.ProviderJobID)
	}
	if owner.UserAddress != "0xUserA" {
		t.Errorf("UserAddress = %q, want 0xUserA", owner.UserAddress)
	}
}

// TestGetVideoJobOwner_NotFound is a regression test for AuthorizeVideoJobAccess's fail-closed
// contract (ctrl/video_ownership.go): a job that was never recorded — an unknown id, or one
// created before ownership tracking existed — must surface as an error the caller treats as
// "deny", not as a zero-value owner that could accidentally compare equal to something.
func TestGetVideoJobOwner_NotFound(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoJobOwnerTable(t, d)

	_, err := d.GetVideoJobOwner("never-created")
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("GetVideoJobOwner error = %v, want gorm.ErrRecordNotFound", err)
	}
}

func TestCreateVideoJobOwner_DuplicateProviderJobIDRejected(t *testing.T) {
	d := setupTestDB(t)
	migrateVideoJobOwnerTable(t, d)

	if err := d.CreateVideoJobOwner("job-dup", "0xUserA"); err != nil {
		t.Fatalf("first CreateVideoJobOwner: %v", err)
	}
	if err := d.CreateVideoJobOwner("job-dup", "0xUserB"); err == nil {
		t.Fatal("expected the second CreateVideoJobOwner with the same provider job id to fail (uniqueIndex)")
	}

	// The original owner must survive a rejected duplicate-key insert attempt.
	owner, err := d.GetVideoJobOwner("job-dup")
	if err != nil {
		t.Fatalf("GetVideoJobOwner: %v", err)
	}
	if owner.UserAddress != "0xUserA" {
		t.Errorf("UserAddress = %q, want 0xUserA (unchanged by the rejected duplicate)", owner.UserAddress)
	}
}
