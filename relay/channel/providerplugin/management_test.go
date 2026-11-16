package providerplugin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderModelCatalogIsolationAndRefresh(t *testing.T) {
	plugin, err := jsplugin.NewRegistry().Register(`
export const meta = {
  apiVersion: 1, key: "catalog-test", name: "Catalog", version: "1.0.0", author: {name: "Test"}, catalogRouting: true,
  relay: [{incoming_path: "/v1/chat/completions", upstream_path: "/chat"}, {incoming_path: "/v1/models", upstream_path: "/models"}]
};`, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(modelCatalogs.Purge)
	var calls atomic.Int32
	var invalid atomic.Bool
	seenKeys := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		seenKeys <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		if invalid.Load() {
			_, _ = w.Write([]byte(`{"data":[{"id":"replacement","routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"https://untrusted.example/chat"}]}]}`))
			return
		}
		body, err := common.Marshal(ModelCatalog{Data: []CatalogModel{{
			ID: fmt.Sprintf("model-%d", call), Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/chat"}},
		}}})
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))
	t.Cleanup(upstream.Close)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelBaseUrl: upstream.URL, ApiKey: "account-one", ChannelOtherSettings: dto.ChannelOtherSettings{AdvancedCustom: plugin.Meta.ChannelRelayConfig(nil)},
	}}
	client := ManagementClient{Override: func(http.Header, string) error { return nil }, Send: upstream.Client().Do}
	first, err := FetchModelCatalog(t.Context(), plugin, info, client, false)
	require.NoError(t, err)
	cached, err := FetchModelCatalog(t.Context(), plugin, info, client, false)
	require.NoError(t, err)
	assert.Equal(t, first.Data, cached.Data)
	assert.Equal(t, int32(1), calls.Load())
	info.ApiKey = "account-two"
	second, err := FetchModelCatalog(t.Context(), plugin, info, client, false)
	require.NoError(t, err)
	assert.NotEqual(t, first.Data[0].ID, second.Data[0].ID)
	assert.Equal(t, "Bearer account-one", <-seenKeys)
	assert.Equal(t, "Bearer account-two", <-seenKeys)
	refreshed, err := FetchModelCatalog(t.Context(), plugin, info, client, true)
	require.NoError(t, err)
	assert.NotEqual(t, second.Data[0].ID, refreshed.Data[0].ID)
	invalid.Store(true)
	_, err = FetchModelCatalog(t.Context(), plugin, info, client, true)
	require.ErrorContains(t, err, "exceeds its manifest")
	_, err = FetchModelCatalog(t.Context(), plugin, info, client, false)
	require.Error(t, err, "a failed refresh must not retain old routes")
	assert.Equal(t, int32(4), calls.Load(), "briefly cache failures instead of hammering the model endpoint")
}

// pagedModelsPlugin exchanges its key for a numbered token, signs every
// request with it and reads the vendor's model list page by page.
const pagedModelsPlugin = `
export const meta = {
  apiVersion: 1, key: "mgmt-paged-models", name: "Paged models", version: "1.0.0", author: {name: "Test"},
  relay: [
    {incoming_path: "/v1/chat/completions", upstream_path: "/chat"},
    {incoming_path: "/v1/models", upstream_path: "/models", converter: "plugin"},
  ],
};
let exchanges = 0;
export function exchangeKey(ctx) {
  exchanges++;
  return {token: {value: ctx.apiKey + "-" + exchanges}, expiresAt: Math.floor(Date.now() / 1000) + 3600};
}
export function signRequest(ctx) { return {headers: {"X-Token": ctx.token.value}}; }
export function convertResponse(ctx, res) {
  const page = JSON.parse(res.body);
  const ids = (ctx.state || []).concat(page.items);
  if (page.next) return {next: {path: "/models?page=" + page.next}, state: ids};
  return {body: {object: "list", data: ids.map((id) => ({id: id, object: "model"}))}};
}
`

// A model list fetch follows the plugin's pages, and a 401 exchanges the key
// again once and sends the rejected page again.
func TestFetchModelListPagesAndExchangesAgainOn401(t *testing.T) {
	plugin, err := jsplugin.NewRegistry().Register(pagedModelsPlugin, jsplugin.Options{})
	require.NoError(t, err)
	var mutex sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("X-Token"))
		mutex.Unlock()
		if r.Header.Get("X-Token") == "sk-paged-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"items":["model-b"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":["model-a"],"next":"2"}`))
	}))
	t.Cleanup(upstream.Close)
	info := &relaycommon.RelayInfo{
		RelayFormat:    types.RelayFormatOpenAI,
		RelayMode:      relayconstant.RelayModeUnknown,
		RequestURLPath: dto.AdvancedCustomModelListPath,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          constant.ChannelTypeTaskPlugin,
			ChannelId:            910_001,
			ChannelBaseUrl:       upstream.URL,
			ApiKey:               "sk-paged",
			ChannelOtherSettings: dto.ChannelOtherSettings{AdvancedCustom: plugin.Meta.ChannelRelayConfig(nil)},
		},
	}

	body, err := FetchModelList(context.Background(), plugin, info, ManagementClient{
		Override: func(http.Header, string) error { return nil },
		Send:     http.DefaultClient.Do,
	})

	require.NoError(t, err)
	assert.JSONEq(t, `{"object":"list","data":[{"id":"model-a","object":"model"},{"id":"model-b","object":"model"}]}`, string(body))
	assert.Equal(t, []string{
		"GET /models sk-paged-1",
		"GET /models sk-paged-2",
		"GET /models?page=2 sk-paged-2",
	}, seen)
}

// A plugin that keeps chaining fails the fetch after MaxFollowUps requests.
func TestFetchModelListStopsAtTheFollowUpLimit(t *testing.T) {
	plugin, err := jsplugin.NewRegistry().Register(`
export const meta = {
  apiVersion: 1, key: "mgmt-endless-models", name: "Endless", version: "1.0.0", author: {name: "Test"},
  relay: [{incoming_path: "/v1/models", upstream_path: "/models", converter: "plugin"}],
};
export function convertResponse() { return {next: {path: "/models"}}; }
`, jsplugin.Options{})
	require.NoError(t, err)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	info := &relaycommon.RelayInfo{
		RelayFormat:    types.RelayFormatOpenAI,
		RequestURLPath: dto.AdvancedCustomModelListPath,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          constant.ChannelTypeTaskPlugin,
			ChannelId:            910_002,
			ChannelBaseUrl:       upstream.URL,
			ApiKey:               "sk-endless",
			ChannelOtherSettings: dto.ChannelOtherSettings{AdvancedCustom: plugin.Meta.ChannelRelayConfig(nil)},
		},
	}

	_, err = FetchModelList(context.Background(), plugin, info, ManagementClient{
		Override: func(http.Header, string) error { return nil },
		Send:     http.DefaultClient.Do,
	})

	require.ErrorContains(t, err, "chained more than 32 model list requests")
	assert.Equal(t, int32(MaxFollowUps+1), requests.Load())
}

// A balance request's url must be on the channel base URL host or a host of
// meta.allowedHosts, as the credential goes with it.
func TestQueryBalanceURLNeedsAnAllowedHost(t *testing.T) {
	var vendorRequests atomic.Int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vendorRequests.Add(1)
		_, _ = w.Write([]byte(`{"usd":7}`))
	}))
	t.Cleanup(vendor.Close)
	base := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(base.Close)
	query := func(t *testing.T, key, allowedHosts string) (jsplugin.BalanceResult, error) {
		plugin, err := jsplugin.NewRegistry().Register(fmt.Sprintf(`
export const meta = {
  apiVersion: 1, key: %q, name: "Balance", version: "1.0.0", author: {name: "Test"}, allowedHosts: %s,
  relay: [{incoming_path: "/v1/chat/completions", upstream_path: "/chat"}],
};
export function buildBalanceRequest() { return {url: %q}; }
export function parseBalance(ctx, res) { return {amount: JSON.parse(res.body).usd, currency: "USD"}; }
`, key, allowedHosts, vendor.URL+"/balance"), jsplugin.Options{})
		require.NoError(t, err)
		info := &relaycommon.RelayInfo{
			RelayFormat: types.RelayFormatOpenAI,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType:          constant.ChannelTypeTaskPlugin,
				ChannelId:            910_003,
				ChannelBaseUrl:       base.URL,
				ApiKey:               "sk-balance",
				ChannelOtherSettings: dto.ChannelOtherSettings{AdvancedCustom: plugin.Meta.ChannelRelayConfig(nil)},
			},
		}
		return QueryBalance(context.Background(), plugin, info, ManagementClient{
			Override: func(http.Header, string) error { return nil },
			Send:     http.DefaultClient.Do,
		})
	}

	t.Run("another host", func(t *testing.T) {
		_, err := query(t, "mgmt-balance-elsewhere", "[]")

		require.ErrorContains(t, err, "is not allowed")
		assert.Zero(t, vendorRequests.Load())
	})

	t.Run("an allowed host", func(t *testing.T) {
		balance, err := query(t, "mgmt-balance-allowed", fmt.Sprintf("[%q]", strings.TrimPrefix(vendor.URL, "http://")))

		require.NoError(t, err)
		require.NotNil(t, balance.Amount)
		assert.Equal(t, 7.0, *balance.Amount)
		assert.Equal(t, int32(1), vendorRequests.Load())
	})
}
