package task

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Banezzz/ezpay/model/data"
	"github.com/Banezzz/ezpay/model/mdb"
	"github.com/Banezzz/ezpay/util/log"
	"github.com/dromara/carbon/v2"
)

const (
	chainlistRPCURL        = "https://chainlist.org/rpcs.json"
	chainlistSyncTimeout   = 90 * time.Second
	chainlistProbeParallel = 6
	chainlistMaxWSPerChain = 12
)

type RpcChainlistSyncJob struct{}

type chainlistChain struct {
	ChainID int64               `json:"chainId"`
	RPCs    []chainlistRPCEntry `json:"rpc"`
}

type chainlistRPCEntry struct {
	URL string
}

func (e *chainlistRPCEntry) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		e.URL = asString
		return nil
	}
	var asObject struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &asObject); err != nil {
		return err
	}
	e.URL = asObject.URL
	return nil
}

type chainlistCandidate struct {
	Network string
	URL     string
}

type chainlistSyncStats struct {
	Tested  int
	Created int
	Updated int
	Down    int
}

func (r RpcChainlistSyncJob) Run() {
	ctx, cancel := context.WithTimeout(context.Background(), chainlistSyncTimeout)
	defer cancel()
	stats, err := SyncChainlistRPCs(ctx, chainlistRPCURL)
	if err != nil {
		log.Sugar.Warnf("[rpc-chainlist] sync failed: %v", err)
		return
	}
	log.Sugar.Infof("[rpc-chainlist] sync complete tested=%d created=%d updated=%d down=%d", stats.Tested, stats.Created, stats.Updated, stats.Down)
}

func SyncChainlistRPCs(ctx context.Context, sourceURL string) (chainlistSyncStats, error) {
	chains, err := fetchChainlist(ctx, sourceURL)
	if err != nil {
		return chainlistSyncStats{}, err
	}
	candidates := collectChainlistWSCandidates(chains)
	if len(candidates) == 0 {
		return chainlistSyncStats{}, nil
	}

	var stats chainlistSyncStats
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, chainlistProbeParallel)

	for _, candidate := range candidates {
		c := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			status, latency := ProbeEVMNode(c.URL)
			mu.Lock()
			stats.Tested++
			mu.Unlock()

			if status != mdb.RpcNodeStatusOk {
				if err := data.UpdateRpcNodeHealthByNetworkURLType(c.Network, c.URL, mdb.RpcNodeTypeWs, status, latency); err != nil {
					log.Sugar.Warnf("[rpc-chainlist] mark down network=%s url=%s err=%v", c.Network, c.URL, err)
				}
				mu.Lock()
				stats.Down++
				mu.Unlock()
				return
			}

			created, err := data.UpsertRpcNodeByNetworkURLType(&mdb.RpcNode{
				Network:       c.Network,
				Url:           c.URL,
				Type:          mdb.RpcNodeTypeWs,
				Weight:        1,
				Enabled:       true,
				Status:        mdb.RpcNodeStatusOk,
				LastLatencyMs: latency,
				LastCheckedAt: *carbon.NewTime(carbon.Now()),
			})
			if err != nil {
				log.Sugar.Warnf("[rpc-chainlist] upsert network=%s url=%s err=%v", c.Network, c.URL, err)
				return
			}

			mu.Lock()
			if created {
				stats.Created++
			} else {
				stats.Updated++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return stats, ctx.Err()
	}
	return stats, nil
}

func fetchChainlist(ctx context.Context, sourceURL string) ([]chainlistChain, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 ezpay-rpc-sync")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("chainlist HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var chains []chainlistChain
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&chains); err != nil {
		return nil, err
	}
	return chains, nil
}

func collectChainlistWSCandidates(chains []chainlistChain) []chainlistCandidate {
	chainIDToNetwork := map[int64]string{
		1:    mdb.NetworkEthereum,
		56:   mdb.NetworkBsc,
		137:  mdb.NetworkPolygon,
		9745: mdb.NetworkPlasma,
	}

	byNetwork := make(map[string][]string)
	seen := make(map[string]struct{})
	for _, chain := range chains {
		network, ok := chainIDToNetwork[chain.ChainID]
		if !ok {
			continue
		}
		for _, rpc := range chain.RPCs {
			rpcURL := normalizeChainlistRPCURL(rpc.URL)
			if rpcURL == "" {
				continue
			}
			key := network + "|" + strings.ToLower(rpcURL)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			byNetwork[network] = append(byNetwork[network], rpcURL)
		}
	}

	networks := make([]string, 0, len(byNetwork))
	for network := range byNetwork {
		networks = append(networks, network)
	}
	sort.Strings(networks)

	out := make([]chainlistCandidate, 0)
	for _, network := range networks {
		urls := byNetwork[network]
		if len(urls) > chainlistMaxWSPerChain {
			urls = urls[:chainlistMaxWSPerChain]
		}
		for _, rpcURL := range urls {
			out = append(out, chainlistCandidate{Network: network, URL: rpcURL})
		}
	}
	return out
}

func normalizeChainlistRPCURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "${") || strings.ContainsAny(raw, "{}<>") {
		return ""
	}
	if strings.Contains(strings.ToLower(raw), "your") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "ws", "wss":
	default:
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u.String()
}
