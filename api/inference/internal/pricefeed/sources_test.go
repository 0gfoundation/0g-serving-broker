package pricefeed

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCoinGeckoSource_ParsesRate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "ids=0g") {
			t.Errorf("expected ids=0g in query, got %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"0g": {"usd": 0.003123}}`))
	}))
	defer srv.Close()

	s := NewCoinGeckoSource(&http.Client{Timeout: time.Second}, srv.URL, "0g", "usd", "", "", "test-ua")
	got, err := s.FetchRate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// big.Rat converts "0.003123" exactly.
	want, _ := new(big.Rat).SetString("0.003123")
	if got.Cmp(want) != 0 {
		t.Errorf("rate = %s, want %s", got.FloatString(6), want.FloatString(6))
	}
}

func TestCoinGeckoSource_MissingCoin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	s := NewCoinGeckoSource(&http.Client{Timeout: time.Second}, srv.URL, "0g", "usd", "", "", "test-ua")
	_, err := s.FetchRate(context.Background())
	if err == nil {
		t.Error("expected error for missing coin in response")
	}
}

func TestCoinGeckoSource_SendsKeyHeader(t *testing.T) {
	tests := []struct {
		name       string
		keyType    string
		wantHeader string
	}{
		{name: "demo", keyType: "demo", wantHeader: "x-cg-demo-api-key"},
		{name: "pro", keyType: "pro", wantHeader: "x-cg-pro-api-key"},
		{name: "default (empty keyType) routes to demo", keyType: "", wantHeader: "x-cg-demo-api-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotHeader, gotValue string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHeader = tt.wantHeader
				gotValue = r.Header.Get(tt.wantHeader)
				_, _ = w.Write([]byte(`{"0g": {"usd": 0.001}}`))
			}))
			defer srv.Close()

			s := NewCoinGeckoSource(&http.Client{Timeout: time.Second}, srv.URL, "0g", "usd", "secret-key", tt.keyType, "test-ua")
			if _, err := s.FetchRate(context.Background()); err != nil {
				t.Fatal(err)
			}
			if gotValue != "secret-key" {
				t.Errorf("header %s = %q, want %q", gotHeader, gotValue, "secret-key")
			}
		})
	}
}

func TestBinanceSource_ParsesPrice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") != "ZGUSDT" {
			t.Errorf("expected symbol=ZGUSDT, got %s", r.URL.Query().Get("symbol"))
		}
		_, _ = w.Write([]byte(`{"symbol":"ZGUSDT","price":"0.00325"}`))
	}))
	defer srv.Close()

	s := NewBinanceSource(&http.Client{Timeout: time.Second}, srv.URL, "ZGUSDT", "test-ua")
	got, err := s.FetchRate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := new(big.Rat).SetString("0.00325")
	if got.Cmp(want) != 0 {
		t.Errorf("rate = %s, want %s", got.FloatString(6), want.FloatString(6))
	}
}

func TestBinanceSource_RejectsNonPositive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"symbol":"ZGUSDT","price":"0"}`))
	}))
	defer srv.Close()

	s := NewBinanceSource(&http.Client{Timeout: time.Second}, srv.URL, "ZGUSDT", "test-ua")
	if _, err := s.FetchRate(context.Background()); err == nil {
		t.Error("expected error for zero price")
	}
}
