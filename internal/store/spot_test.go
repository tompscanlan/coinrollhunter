package store

import (
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

func TestLatestSpotUsesCalendarDateAndManualCorrections(t *testing.T) {
	s := txStore(t)

	for _, spot := range []model.Spot{
		{AsOf: "2026-07-23T02:00:00Z", GoldUSD: 4000, Source: "poller"},
		{AsOf: "2026-07-23", GoldUSD: 3950, Source: "manual"},
	} {
		if err := s.PutSpot(spot); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.LatestSpot()
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "manual" || got.GoldUSD != 3950 {
		t.Fatalf("same-day latest = %+v, want manual correction", got)
	}
	observation, err := s.LatestSpotObservation()
	if err != nil {
		t.Fatal(err)
	}
	if observation.AsOf != "2026-07-23T02:00:00Z" {
		t.Fatalf("freshness observation = %+v, want same-day poller timestamp", observation)
	}

	if err := s.PutSpot(model.Spot{AsOf: "2026-07-24T01:00:00Z", GoldUSD: 4050, Source: "poller"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.LatestSpot()
	if err != nil {
		t.Fatal(err)
	}
	if got.AsOf != "2026-07-24T01:00:00Z" {
		t.Fatalf("latest = %+v, want observation from newer calendar day", got)
	}
}
