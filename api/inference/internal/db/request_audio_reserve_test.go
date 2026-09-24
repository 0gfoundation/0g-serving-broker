//go:build integration

package db

import (
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/model"
)

// seedAudioReservedRequest writes an audio request row the way proxy.go creates
// one: the in-flight reserve in fee, nothing billed yet (output_count = 0).
func seedAudioReservedRequest(t *testing.T, d *DB, requestHash, reserve string) {
	t.Helper()
	req := model.Request{
		UserAddress: "0xUser",
		Nonce:       requestHash,
		RequestHash: requestHash,
		ServiceName: "audio-generation",
		InputFee:    "0",
		OutputFee:   "0",
		Fee:         reserve,
	}
	if err := d.db.Create(&req).Error; err != nil {
		t.Fatalf("seed request %s: %v", requestHash, err)
	}
}

// TestReleaseUnbilledRequestReserve pins the guard the audio release rests on.
// proxy.go calls it unconditionally once a request is over, so it must tell a
// billed row from an unbilled one on its own: clear the reserve when nothing
// billed, and leave a real fee untouched when something did.
func TestReleaseUnbilledRequestReserve(t *testing.T) {
	const reserve = "120000000000000000000"

	t.Run("the reserve is counted while the request is in flight", func(t *testing.T) {
		d := setupTestDB(t)
		migrateUsageTables(t, d)
		seedAudioReservedRequest(t, d, "audio-inflight", reserve)

		unsettled, err := d.CalculateUnsettledFee("0xUser")
		if err != nil {
			t.Fatalf("CalculateUnsettledFee: %v", err)
		}
		if unsettled.String() != reserve {
			t.Errorf("unsettled = %s, want the reserve %s — a reserve nobody counts gates nothing", unsettled, reserve)
		}
		// And it is never settleable: output_count is still 0.
		list, _, err := d.ListRequest(model.RequestListOptions{Processed: false, ExcludeZeroOutput: true})
		if err != nil {
			t.Fatalf("ListRequest: %v", err)
		}
		if len(list) != 0 {
			t.Errorf("an unbilled audio reserve is settleable: %+v", list)
		}
	})

	t.Run("an unbilled request has its reserve released", func(t *testing.T) {
		d := setupTestDB(t)
		migrateUsageTables(t, d)
		seedAudioReservedRequest(t, d, "audio-failed", reserve)

		released, err := d.ReleaseUnbilledRequestReserve("audio-failed")
		if err != nil {
			t.Fatalf("ReleaseUnbilledRequestReserve: %v", err)
		}
		if !released {
			t.Error("released = false for an unbilled row holding a reserve")
		}
		unsettled, err := d.CalculateUnsettledFee("0xUser")
		if err != nil {
			t.Fatalf("CalculateUnsettledFee: %v", err)
		}
		if unsettled.Sign() != 0 {
			t.Errorf("unsettled = %s after release, want 0 — a failed request kept locking the wallet's balance", unsettled)
		}
	})

	// The case the guard exists for. A bill writes a non-zero output_count; the
	// release that follows it must be a no-op, or every successful request would
	// have its fee wiped and be served free.
	t.Run("a billed request keeps its fee", func(t *testing.T) {
		d := setupTestDB(t)
		migrateUsageTables(t, d)
		seedAudioReservedRequest(t, d, "audio-billed", reserve)
		const billed = "47000000000000000000"
		if err := d.UpdateRequestFeesAndCount("audio-billed", billed, billed, 47); err != nil {
			t.Fatalf("UpdateRequestFeesAndCount: %v", err)
		}

		released, err := d.ReleaseUnbilledRequestReserve("audio-billed")
		if err != nil {
			t.Fatalf("ReleaseUnbilledRequestReserve: %v", err)
		}
		if released {
			t.Error("released = true for a billed row")
		}
		req, err := d.GetRequest("audio-billed")
		if err != nil {
			t.Fatalf("GetRequest: %v", err)
		}
		if req.Fee != billed {
			t.Errorf("fee = %q after release, want the billed %q — the release wiped a real charge", req.Fee, billed)
		}
	})

	t.Run("an unknown or empty hash is not an error", func(t *testing.T) {
		d := setupTestDB(t)
		migrateUsageTables(t, d)
		for _, hash := range []string{"", "no-such-request"} {
			released, err := d.ReleaseUnbilledRequestReserve(hash)
			if err != nil || released {
				t.Errorf("ReleaseUnbilledRequestReserve(%q) = (%v, %v), want (false, nil)", hash, released, err)
			}
		}
	})
}
