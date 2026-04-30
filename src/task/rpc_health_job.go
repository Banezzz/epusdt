package task

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Banezzz/ezpay/model/data"
	"github.com/Banezzz/ezpay/model/mdb"
	"github.com/Banezzz/ezpay/util/log"

	"github.com/ethereum/go-ethereum/ethclient"
)

const rpcProbeTimeout = 5 * time.Second

// RpcHealthJob periodically probes every enabled rpc_nodes row and
// writes status/last_latency_ms. Results drive SelectRpcNode weighted
// picking at runtime.
type RpcHealthJob struct{}

var gRpcHealthJobLock sync.Mutex

func (r RpcHealthJob) Run() {
	gRpcHealthJobLock.Lock()
	defer gRpcHealthJobLock.Unlock()

	nodes, err := data.ListRpcNodes("")
	if err != nil {
		log.Sugar.Errorf("[rpc-health] list nodes err=%v", err)
		return
	}
	var wg sync.WaitGroup
	for i := range nodes {
		if !nodes[i].Enabled {
			continue
		}
		wg.Add(1)
		go func(n mdb.RpcNode) {
			defer wg.Done()
			status, latency := ProbeRpcNode(n)
			if err := data.UpdateRpcNodeHealth(n.ID, status, latency); err != nil {
				log.Sugar.Warnf("[rpc-health] update node %d err=%v", n.ID, err)
			}
		}(nodes[i])
	}
	wg.Wait()
}

// ProbeRpcNode performs a network-aware health probe. EVM endpoints must
// answer eth_blockNumber, Tron must answer getnowblock, Solana must
// answer getHealth/getSlot. Unknown networks fall back to a TCP probe.
func ProbeRpcNode(node mdb.RpcNode) (string, int) {
	network := strings.ToLower(strings.TrimSpace(node.Network))
	switch network {
	case mdb.NetworkEthereum, mdb.NetworkBsc, mdb.NetworkPolygon, mdb.NetworkPlasma:
		return ProbeEVMNode(node.Url)
	case mdb.NetworkTron:
		return ProbeTronNode(node.Url, node.ApiKey)
	case mdb.NetworkSolana:
		return ProbeSolanaNode(node.Url)
	default:
		return ProbeNode(node.Url)
	}
}

// ProbeNode does a TCP dial to the RPC URL and returns (status, latencyMs).
// Exported so the admin controller can reuse it without duplicating logic.
func ProbeNode(rawURL string) (string, int) {
	addr, err := ParseAddress(rawURL)
	if err != nil {
		return mdb.RpcNodeStatusDown, -1
	}
	dur, err := MeasureTCPDial(addr, rpcProbeTimeout)
	if err != nil {
		return mdb.RpcNodeStatusDown, -1
	}
	return mdb.RpcNodeStatusOk, int(dur.Milliseconds())
}

// ProbeEVMNode verifies an HTTP or WebSocket EVM RPC endpoint by calling
// eth_blockNumber through go-ethereum's client.
func ProbeEVMNode(rawURL string) (string, int) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return mdb.RpcNodeStatusDown, -1
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcProbeTimeout)
	defer cancel()

	start := time.Now()
	client, err := ethclient.DialContext(ctx, rawURL)
	if err != nil {
		return mdb.RpcNodeStatusDown, -1
	}
	defer client.Close()
	if _, err = client.BlockNumber(ctx); err != nil {
		return mdb.RpcNodeStatusDown, -1
	}
	return mdb.RpcNodeStatusOk, int(time.Since(start).Milliseconds())
}

func ProbeTronNode(rawURL, apiKey string) (string, int) {
	baseURL := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if baseURL == "" {
		return mdb.RpcNodeStatusDown, -1
	}
	headers := map[string]string{}
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		headers["TRON-PRO-API-KEY"] = apiKey
	}
	status, latency := probeJSONPost(baseURL+"/wallet/getnowblock", map[string]interface{}{}, headers, func(body []byte) bool {
		var resp struct {
			BlockID     string `json:"blockID"`
			BlockHeader struct {
				RawData struct {
					Number int64 `json:"number"`
				} `json:"raw_data"`
			} `json:"block_header"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return false
		}
		return resp.BlockID != "" || resp.BlockHeader.RawData.Number > 0
	})
	return status, latency
}

func ProbeSolanaNode(rawURL string) (string, int) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return mdb.RpcNodeStatusDown, -1
	}
	status, latency := probeSolanaMethod(rawURL, "getHealth")
	if status == mdb.RpcNodeStatusOk {
		return status, latency
	}
	return probeSolanaMethod(rawURL, "getSlot")
}

func probeSolanaMethod(rawURL, method string) (string, int) {
	return probeJSONPost(rawURL, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}, nil, func(body []byte) bool {
		var resp struct {
			Result interface{} `json:"result"`
			Error  interface{} `json:"error"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return false
		}
		return resp.Error == nil && resp.Result != nil
	})
}

func probeJSONPost(rawURL string, body interface{}, headers map[string]string, validate func([]byte) bool) (string, int) {
	payload, err := json.Marshal(body)
	if err != nil {
		return mdb.RpcNodeStatusDown, -1
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(payload))
	if err != nil {
		return mdb.RpcNodeStatusDown, -1
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return mdb.RpcNodeStatusDown, -1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return mdb.RpcNodeStatusDown, -1
	}
	if validate != nil && !validate(respBody) {
		return mdb.RpcNodeStatusDown, -1
	}
	return mdb.RpcNodeStatusOk, int(time.Since(start).Milliseconds())
}

func ParseAddress(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "tcp://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https", "wss":
			port = "443"
		case "http", "ws", "tcp":
			port = "80"
		default:
			return "", fmt.Errorf("unsupported URL scheme %q", u.Scheme)
		}
	}

	return host + ":" + port, nil
}

func MeasureTCPDial(addr string, timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	return time.Since(start), nil
}
