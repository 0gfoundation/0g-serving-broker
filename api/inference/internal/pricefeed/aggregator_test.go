package pricefeed_test

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed/pricefeedtest"
)

func rat(s string) *big.Rat {
	r, _ := new(big.Rat).SetString(s)
	return r
}

func TestAggregator_MedianThreeHealthy(t *testing.T) {
	srcs := []pricefeed.Source{
		pricefeedtest.NewMockSource("a", rat("0.0030")),
		pricefeedtest.NewMockSource("b", rat("0.0031")),
		pricefeedtest.NewMockSource("c", rat("0.0032")),
	}
	agg := pricefeed.NewAggregator(srcs, 2, 500, time.Second, nil)
	got, _, err := agg.Aggregate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(rat("0.0031")) != 0 {
		t.Errorf("median = %s, want 0.0031", got.FloatString(6))
	}
}

func TestAggregator_EvenNumberTakesMean(t *testing.T) {
	srcs := []pricefeed.Source{
		pricefeedtest.NewMockSource("a", rat("0.0030")),
		pricefeedtest.NewMockSource("b", rat("0.0032")),
	}
	agg := pricefeed.NewAggregator(srcs, 2, 500, time.Second, nil)
	got, _, err := agg.Aggregate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Mean of 0.0030 and 0.0032 = 0.0031
	if got.Cmp(rat("0.0031")) != 0 {
		t.Errorf("median = %s, want 0.0031", got.FloatString(6))
	}
}

func TestAggregator_DropsOutlier(t *testing.T) {
	// Two clustered sources around 0.003 plus one outlier at 0.01 (~233% deviation).
	// With maxDeviationBps=500 (5%), the outlier must be dropped.
	srcs := []pricefeed.Source{
		pricefeedtest.NewMockSource("a", rat("0.0030")),
		pricefeedtest.NewMockSource("b", rat("0.0031")),
		pricefeedtest.NewMockSource("c", rat("0.01")),
	}
	agg := pricefeed.NewAggregator(srcs, 2, 500, time.Second, nil)
	got, quotes, err := agg.Aggregate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Remaining {a, b} => median = 0.00305
	if got.Cmp(rat("0.00305")) != 0 {
		t.Errorf("post-outlier median = %s, want 0.00305", got.FloatString(6))
	}
	if len(quotes) != 3 {
		t.Errorf("expected 3 quotes (including outlier) for logging, got %d", len(quotes))
	}
}

func TestAggregator_QuorumNotMet(t *testing.T) {
	failing := pricefeedtest.NewMockSource("a", nil)
	failing.SetError(errors.New("boom"))
	srcs := []pricefeed.Source{
		failing,
		pricefeedtest.NewMockSource("b", rat("0.0031")),
	}
	agg := pricefeed.NewAggregator(srcs, 2, 500, time.Second, nil)
	_, _, err := agg.Aggregate(context.Background())
	if err == nil {
		t.Error("expected quorum-not-met error with only 1 healthy source and minQuorum=2")
	}
}

func TestAggregator_AllSourcesFail(t *testing.T) {
	a := pricefeedtest.NewMockSource("a", nil)
	a.SetError(errors.New("x"))
	b := pricefeedtest.NewMockSource("b", nil)
	b.SetError(errors.New("y"))
	srcs := []pricefeed.Source{a, b}
	agg := pricefeed.NewAggregator(srcs, 1, 500, time.Second, nil)
	_, quotes, err := agg.Aggregate(context.Background())
	if err == nil {
		t.Error("expected error when all sources fail")
	}
	if len(quotes) != 2 {
		t.Errorf("expected 2 quotes for logging, got %d", len(quotes))
	}
}

func TestAggregator_NonPositiveRateTreatedAsUnhealthy(t *testing.T) {
	srcs := []pricefeed.Source{
		pricefeedtest.NewMockSource("a", rat("0")), // non-positive — must be filtered
		pricefeedtest.NewMockSource("b", rat("0.0031")),
		pricefeedtest.NewMockSource("c", rat("0.0030")),
	}
	agg := pricefeed.NewAggregator(srcs, 2, 500, time.Second, nil)
	got, _, err := agg.Aggregate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// {b, c} => median = 0.00305
	if got.Cmp(rat("0.00305")) != 0 {
		t.Errorf("median = %s, want 0.00305", got.FloatString(6))
	}
}

func TestAggregator_RespectsTimeout(t *testing.T) {
	srcs := []pricefeed.Source{pricefeedtest.NewMockSource("a", rat("0.0030"))}
	agg := pricefeed.NewAggregator(srcs, 1, 500, 50*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, _, err := agg.Aggregate(ctx)
	if err == nil {
		t.Error("expected error when context is already cancelled")
	}
}
