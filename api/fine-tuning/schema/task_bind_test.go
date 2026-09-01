package schema

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// UserAddress reaches filepath.Join as a directory name, and common.HexToAddress
// is not a validator: hex.DecodeString keeps its partial output, so an address
// with path segments glued on compares EQUAL to the bare address through
// HexToAddress while escaping the data directory when joined.
func TestTaskBindRejectsNonAddressUserAddress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const valid = "0x1111111111111111111111111111111111111111"

	body := func(userAddress string) string {
		return `{"userAddress":"` + userAddress + `","preTrainedModelHash":"0xaa","datasetHash":"0xbb",` +
			`"trainingParams":"{}","fee":"1","nonce":"1","signature":"0xcc"}`
	}
	bind := func(t *testing.T, userAddress string) error {
		t.Helper()
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(body(userAddress)))
		ctx.Request.Header.Set("Content-Type", "application/json")
		var task Task
		return task.Bind(ctx)
	}

	if err := bind(t, valid); err != nil {
		t.Fatalf("a plain hex address was rejected: %v", err)
	}

	rejected := []string{
		valid + "/../..",   // escapes datasets/ once joined
		valid + "/..%2f..", // the same with an encoded separator
		"0x1111",           // too short
		strings.ToUpper(valid[2:]) + "zz",
		"not-an-address",
	}
	for _, bad := range rejected {
		if err := bind(t, bad); err == nil {
			t.Errorf("userAddress %q was accepted; it reaches filepath.Join as a directory name", bad)
		}
	}
}
