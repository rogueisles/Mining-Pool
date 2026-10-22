package main

import (
	"encoding/json"
	"testing"
)

func twoChainConfig(t *testing.T) config {
	t.Helper()
	c := validConfig()
	if err := json.Unmarshal([]byte(`{"externalChainPolicy":"dgb-doge"}`), &c); err != nil {
		t.Fatal(err)
	}
	for _, symbol := range []string{"DGB", "DOGE"} {
		ep := c.Digibyte
		ep.Genesis = multiGenesis[symbol]
		script := "0014706267a5255e3666936bcb9a777667599f387546"
		if symbol == "DOGE" {
			script = c.DogeScript
		}
		c.Multi = append(c.Multi, multiEndpoint{Symbol: symbol, ChainID: multiIDs[symbol], PayoutScript: script, endpoint: ep})
	}
	return c
}

func TestTwoChainConfiguration(t *testing.T) {
	if err := validateMultiConfig(twoChainConfig(t)); err != nil {
		t.Fatal("explicit two-chain configuration rejected:", err)
	}
}

func TestTwoChainConfigurationRejectsExtraMissingAndDuplicateChains(t *testing.T) {
	for _, mutation := range []func(*config){
		func(c *config) { c.Multi = c.Multi[:1] },
		func(c *config) { c.Multi[1] = c.Multi[0] },
		func(c *config) {
			c.Multi[1].Symbol = "PEP"
			c.Multi[1].ChainID = multiIDs["PEP"]
			c.Multi[1].Genesis = multiGenesis["PEP"]
		},
		func(c *config) { c.Multi = append(c.Multi, c.Multi[0]) },
		func(c *config) { json.Unmarshal([]byte(`{"externalChainPolicy":"unknown"}`), c) },
	} {
		c := twoChainConfig(t)
		mutation(&c)
		if validateMultiConfig(c) == nil {
			t.Fatal("invalid two-chain configuration accepted")
		}
	}
}
