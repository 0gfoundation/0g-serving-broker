package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

func quoteHandler(t *testing.T, ts *tee.TeeService) *Handler {
	t.Helper()
	l, err := log.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "error"})
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{ctrl: ctrl.New(nil, nil, &config.Config{}, nil, ts, nil, l), logger: l}
}

func getQuote(h *Handler, query string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/quote"+query, nil)
	h.GetQuote(ctx)
	return w
}

// report_data_version=2 returns the state quote and nothing else: a client asking for the
// state commitment must never be handed a quote that commits to something different.
func TestGetQuoteReportDataVersion2(t *testing.T) {
	h := quoteHandler(t, &tee.TeeService{Quote: "legacy", QuoteV2: "v1", QuoteState: "state", QuoteStateSig: []byte{1}})
	if w := getQuote(h, "?report_data_version=2"); w.Code != http.StatusOK || w.Body.String() != "state" || w.Header().Get(quoteSignatureHeader) == "" {
		t.Errorf("v2 = %d %q sig=%q", w.Code, w.Body.String(), w.Header().Get(quoteSignatureHeader))
	}
	if w := getQuote(h, "?report_data_version=2&legacy=false"); w.Body.String() != "state" {
		t.Errorf("v2 with legacy=false = %q, want the state quote", w.Body.String())
	}
	if w := getQuote(h, "?report_data_version=3"); w.Code != http.StatusBadRequest {
		t.Errorf("version 3 = %d, want 400", w.Code)
	}
	if w := getQuote(h, "?legacy=false"); w.Body.String() != "v1" {
		t.Errorf("legacy=false = %q, want unchanged", w.Body.String())
	}

	none := quoteHandler(t, &tee.TeeService{Quote: "legacy", QuoteV2: "v1"})
	if w := getQuote(none, "?report_data_version=2"); w.Code != http.StatusNotFound {
		t.Errorf("v2 without a state quote = %d %q, want 404, not a fallback", w.Code, w.Body.String())
	}
}
