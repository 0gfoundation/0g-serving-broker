package providercontract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestIsPreviousDeliverableNotAcknowledged_DirectError ensures the helper
// recognises the bare sentinel and any wrapping done via fmt.Errorf("%w").
func TestIsPreviousDeliverableNotAcknowledged_DirectError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"sentinel only", ErrPreviousDeliverableNotAcknowledged, true},
		{"wrapped with %w", fmt.Errorf("validate addDeliverable: %w", ErrPreviousDeliverableNotAcknowledged), true},
		{"wrapped twice", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", ErrPreviousDeliverableNotAcknowledged)), true},
		{"unrelated error", fmt.Errorf("invalid signature"), false},
		{"deliverable already exists", ErrDeliverableAlreadyExists, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPreviousDeliverableNotAcknowledged(tc.err); got != tc.want {
				t.Errorf("IsPreviousDeliverableNotAcknowledged(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestDecorateAddDeliverableErr_AttachesActionableHint verifies that the
// decorator surfaces a remediation hint when (and only when) the underlying
// error is the unack sentinel. The hint is what the May 2026 bug report
// flagged as missing — see Bug #4.
func TestDecorateAddDeliverableErr_AttachesActionableHint(t *testing.T) {
	user := common.HexToAddress("0x1d4D51F08ab86985533Da9D574A3df68336c485D")

	cases := []struct {
		name        string
		err         error
		wantContain []string
		wantNotChng bool // when the decorator should pass through unchanged
	}{
		{
			name: "unack failure gets remediation hint",
			err:  fmt.Errorf("%w: id=33dc2c37-f51e-428f-9ec2-e7f90eced595", ErrPreviousDeliverableNotAcknowledged),
			wantContain: []string{
				"previous deliverable not acknowledged",
				"id=33dc2c37-f51e-428f-9ec2-e7f90eced595",
				"acknowledgeDeliverable",
				strings.ToLower(user.Hex()), // user address spelled out (case-insensitive substring check)
			},
		},
		{
			name:        "unrelated error untouched",
			err:         fmt.Errorf("rpc connection refused"),
			wantNotChng: true,
		},
		{
			name:        "nil error untouched",
			err:         nil,
			wantNotChng: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decorateAddDeliverableErr(tc.err, user)
			if tc.wantNotChng {
				if got != tc.err {
					t.Errorf("decorate should be a no-op for non-unack errors, got %v, want %v", got, tc.err)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected decorated error, got nil")
			}
			gotMsg := strings.ToLower(got.Error())
			for _, sub := range tc.wantContain {
				if !strings.Contains(gotMsg, strings.ToLower(sub)) {
					t.Errorf("decorated message missing %q\nfull message: %s", sub, got.Error())
				}
			}
			// Decorated error must still match the original sentinel via errors.Is
			// so callers can use IsPreviousDeliverableNotAcknowledged on it.
			if !IsPreviousDeliverableNotAcknowledged(got) {
				t.Errorf("decorated error broke the errors.Is chain — IsPreviousDeliverableNotAcknowledged returned false")
			}
		})
	}
}

func TestIsDeliverableIdInvalidLength(t *testing.T) {
	if !IsDeliverableIdInvalidLength(ErrDeliverableIdInvalidLength) {
		t.Error("expected sentinel match")
	}
	if !IsDeliverableIdInvalidLength(fmt.Errorf("validate: %w", ErrDeliverableIdInvalidLength)) {
		t.Error("expected wrapped match")
	}
	if IsDeliverableIdInvalidLength(fmt.Errorf("unrelated")) {
		t.Error("unrelated error should not match")
	}
}
