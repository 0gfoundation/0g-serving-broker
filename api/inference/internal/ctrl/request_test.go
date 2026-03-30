package ctrl

import (
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func TestBalanceSufficient(t *testing.T) {
	min, _ := new(big.Int).SetString(constant.MinimumLockedBalance, 10)
	zero := big.NewInt(0)
	one := big.NewInt(1)

	tests := []struct {
		name         string
		lockBalance  *big.Int
		inputFee     *big.Int
		unsettledFee *big.Int
		minReserve   *big.Int
		want         bool
	}{
		{
			name:         "exact minimum, zero fees",
			lockBalance:  new(big.Int).Set(min),
			inputFee:     zero,
			unsettledFee: zero,
			minReserve:   min,
			want:         true,
		},
		{
			// T2: regression test — this case had no coverage when MinimumLockedBalance was raised
			// from 1 to 5 0G. Any future change to the constant will fail this test.
			name:         "one below minimum, zero fees",
			lockBalance:  new(big.Int).Sub(min, one),
			inputFee:     zero,
			unsettledFee: zero,
			minReserve:   min,
			want:         false,
		},
		{
			name:         "zero balance",
			lockBalance:  zero,
			inputFee:     zero,
			unsettledFee: zero,
			minReserve:   min,
			want:         false,
		},
		{
			name:         "one above minimum, zero fees",
			lockBalance:  new(big.Int).Add(min, one),
			inputFee:     zero,
			unsettledFee: zero,
			minReserve:   min,
			want:         true,
		},
		{
			name:         "unsettled fee eats into reserve",
			lockBalance:  new(big.Int).Set(min),
			inputFee:     zero,
			unsettledFee: one,
			minReserve:   min,
			want:         false,
		},
		{
			name:         "balance exactly covers combined total",
			lockBalance:  new(big.Int).Add(min, big.NewInt(150)),
			inputFee:     big.NewInt(100),
			unsettledFee: big.NewInt(50),
			minReserve:   min,
			want:         true,
		},
		{
			name:         "balance one below combined total",
			lockBalance:  new(big.Int).Add(min, big.NewInt(149)),
			inputFee:     big.NewInt(100),
			unsettledFee: big.NewInt(50),
			minReserve:   min,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := balanceSufficient(tt.lockBalance, tt.inputFee, tt.unsettledFee, tt.minReserve)
			if got != tt.want {
				t.Errorf("balanceSufficient() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateBalanceAdequacyNilLockBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	c := &Ctrl{}
	account := model.User{} // LockBalance is nil

	err := c.validateBalanceAdequacy(ctx, account, "0")
	if err == nil {
		t.Fatal("expected error for nil LockBalance, got nil")
	}
	if !strings.Contains(err.Error(), "nil lockBalance") {
		t.Errorf("unexpected error: %v", err)
	}
}
