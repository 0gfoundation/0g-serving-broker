package ctrl

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The assay's answers decide what gets paid: a 30x must not hand the
// conversation to another host (the shared client follows redirects).
func TestAssayDoRefusesRedirects(t *testing.T) {
	leak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect target was contacted: %s", r.URL)
	}))
	defer leak.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, leak.URL+"/v1/payout/invoice", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c := &Ctrl{httpClient: srv.Client()}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/payout/invoice", strings.NewReader("{}"))
	resp, err := c.assayDo(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("redirect was followed")
	}
	if !strings.Contains(err.Error(), "redirects are not followed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.httpClient.CheckRedirect != nil {
		t.Fatal("shared client must stay untouched")
	}
}
