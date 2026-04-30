package task

import (
	"encoding/json"
	"testing"

	"github.com/Banezzz/ezpay/model/mdb"
)

func TestChainlistRPCEntryUnmarshalSupportsStringAndObject(t *testing.T) {
	var entries []chainlistRPCEntry
	if err := json.Unmarshal([]byte(`["wss://one.example",{"url":"wss://two.example"}]`), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].URL != "wss://one.example" || entries[1].URL != "wss://two.example" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
}

func TestCollectChainlistWSCandidatesFiltersSupportedNetworks(t *testing.T) {
	chains := []chainlistChain{
		{
			ChainID: 56,
			RPCs: []chainlistRPCEntry{
				{URL: "https://bsc.example"},
				{URL: "wss://BSC.example/ws"},
				{URL: "wss://BSC.example/ws"},
				{URL: "wss://needs-key.example/${API_KEY}"},
			},
		},
		{
			ChainID: 999999,
			RPCs: []chainlistRPCEntry{
				{URL: "wss://ignored.example"},
			},
		},
	}

	got := collectChainlistWSCandidates(chains)
	if len(got) != 1 {
		t.Fatalf("len(candidates) = %d, want 1: %#v", len(got), got)
	}
	if got[0].Network != mdb.NetworkBsc {
		t.Fatalf("network = %q, want %q", got[0].Network, mdb.NetworkBsc)
	}
	if got[0].URL != "wss://bsc.example/ws" {
		t.Fatalf("url = %q, want normalized wss://bsc.example/ws", got[0].URL)
	}
}
