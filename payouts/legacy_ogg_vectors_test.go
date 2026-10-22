package payouts

import (
	"encoding/json"
	"io/ioutil"
	"testing"
)

type legacyOGGVector struct {
	Height          int64            `json:"height"`
	MinerReward     string           `json:"minerRewardWei"`
	InclusionReward string           `json:"inclusionRewardWei"`
	UncleRewards    map[int64]string `json:"uncleRewardsWei"`
}

func legacyOGGVectorAt(t *testing.T, height int64) legacyOGGVector {
	t.Helper()
	data, err := ioutil.ReadFile("testdata/legacy-ogg-uncle-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Vectors []legacyOGGVector `json:"vectors"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, v := range fixture.Vectors {
		if v.Height == height {
			return v
		}
	}
	t.Fatal("missing independent legacy vector", height)
	return legacyOGGVector{}
}
