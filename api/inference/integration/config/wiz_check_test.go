package main

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWizardParsesUpdatedTemplates(t *testing.T) {
	for _, p := range []string{
		"config.testnet.yml",
		"config.mainnet.yml",
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		var c Config
		if err := yaml.Unmarshal(data, &c); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if c.Network == nil {
			t.Errorf("%s: expected Network to be set", p)
			continue
		}
		if c.Network.URL == "" {
			t.Errorf("%s: Network.URL empty", p)
		}
		if c.Database.DSN == "" {
			t.Errorf("%s: Database.DSN empty", p)
		}
		if c.Event.ListenAddr == "" {
			t.Errorf("%s: Event.ListenAddr empty", p)
		}
		if c.Interval.AutoSettleBufferTime == "" {
			t.Errorf("%s: Interval.AutoSettleBufferTime empty", p)
		}
	}
}
