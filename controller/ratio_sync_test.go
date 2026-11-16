package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay/channel/providerplugin"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type copilotPricingTransport func(*http.Request) (*http.Response, error)

func (transport copilotPricingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestCopilotPricingRefreshFailuresPreserveVerifiedPrices(t *testing.T) {
	database := modelManagementDB(t, "sqlite", "")
	require.NoError(t, database.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}))
	source, err := plugins.Source("github-copilot")
	require.NoError(t, err)
	_, err = jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jsplugin.DefaultRegistry.Unregister("github-copilot")) })
	credential, err := (jsplugin.Credential{Plugin: "github-copilot", Data: []byte(`{"copilotToken":"test-only","apiOrigin":"https://api.githubcopilot.com"}`)}).Line()
	require.NoError(t, err)
	channel := model.Channel{Type: 61, Name: "Copilot test", Key: credential, Status: 1, BaseURL: new("https://api.githubcopilot.com"), Setting: new(`{"task_plugin_key":"github-copilot"}`)}
	require.NoError(t, database.Create(&channel).Error)
	service.InitHttpClient()
	client := service.GetHttpClient()
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	const markdown = `All prices are **per 1 million tokens**.
| Model | Input | Cached input | Output |
| --- | --- | --- | --- |
| Future Model | $1 | $0.1 | $5 |
`
	mode := "valid"
	client.Transport = copilotPricingTransport(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "https", request.URL.Scheme)
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: request}
		if request.URL.Host == "api.githubcopilot.com" {
			assert.Equal(t, "/models", request.URL.Path)
			assert.Equal(t, "Bearer test-only", request.Header.Get("Authorization"))
			response.Header.Set("Content-Type", "application/json")
			response.Body = io.NopCloser(strings.NewReader(`{"data":[{"id":"future-model","name":"Future Model","capabilities":{"type":"chat"},"supported_endpoints":["/responses"]}]}`))
			return response, nil
		}
		require.Equal(t, copilotPricingSource, request.URL.String(), "pricing must use only the pinned public source")
		assert.Empty(t, request.Header.Get("Authorization"), "the public documentation must not receive the Copilot credential")
		switch mode {
		case "outage":
			return nil, errors.New("network unavailable")
		case "redirect":
			response.StatusCode = http.StatusFound
			response.Header.Set("Location", "https://untrusted.invalid/prices")
			response.Body = io.NopCloser(strings.NewReader(""))
		case "invalid":
			response.Body = io.NopCloser(strings.NewReader(strings.Replace(markdown, "$1", "$NaN", 1)))
		default:
			response.Body = io.NopCloser(strings.NewReader(markdown))
		}
		return response, nil
	})
	upstream := dto.UpstreamDTO{ID: channel.Id, BaseURL: channel.GetBaseURL()}
	pricesBefore := billing_setting.GetBillingExprCopy()
	data, err := fetchCopilotPrices(t.Context(), upstream)
	require.NoError(t, err)
	require.NotEmpty(t, data["copilot_sources"])
	state, err := model.GetCopilotPricingState()
	require.NoError(t, err)
	verified := state.Sources[channel.Id]
	for _, failure := range []string{"outage", "redirect", "invalid"} {
		mode = failure
		data, err := fetchCopilotPrices(t.Context(), upstream)
		require.Error(t, err, failure)
		assert.Nil(t, data)
		state, err = model.GetCopilotPricingState()
		require.NoError(t, err)
		failed := state.Sources[channel.Id]
		assert.NotEmpty(t, failed.Error, "failure must remain visible")
		assert.Equal(t, verified.Version, failed.Version)
		assert.Equal(t, verified.LastSuccessAt, failed.LastSuccessAt)
		assert.Equal(t, verified.Expressions, failed.Expressions)
		assert.Equal(t, pricesBefore, billing_setting.GetBillingExprCopy(), "source checks must never write charge rates")
	}
}

func TestCopilotPricingVerificationDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			database := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, database.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}))
			const alias = "github-copilot/future-model"
			const expression = `tier("standard", p * 1 + c * 5)`
			const changedExpression = `tier("standard", p * 2 + c * 10)`
			now := common.GetTimestamp() - 300
			require.ErrorContains(t, model.CheckCopilotPricing(1, alias, "future-model", now), "not been verified")
			require.NoError(t, model.RecordCopilotPricingCheck(t.Context(), 1, strings.Repeat("1", 64), map[string]string{"future-model": expression}, nil, time.Unix(now, 0)))
			require.ErrorContains(t, model.CheckCopilotPricing(1, alias, "future-model", now), "needs review")

			snapshot, err := model.GetModelPricingSnapshot([]string{alias})
			require.NoError(t, err)
			reference := model.CopilotPricingReference{ChannelID: 1, Model: "future-model", Version: model.CopilotPricingVersion("future-model", expression)}
			change := model.ModelPricingChange{
				ModelName: alias, ExpectedVersion: snapshot.EmptyVersion,
				Pricing:       model.PricingValues{"billing_setting.billing_mode": "tiered_expr", "billing_setting.billing_expr": expression},
				CopilotSource: &reference,
			}
			require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{change}))
			assert.NoError(t, model.CheckCopilotPricing(1, alias, "future-model", now))
			assert.ErrorContains(t, model.CheckCopilotPricing(1, alias, "different-model", now), "needs review")
			assert.Error(t, model.CheckCopilotPricing(2, alias, "future-model", now), "another channel cannot borrow the refresh")
			require.NoError(t, model.RecordCopilotPricingCheck(t.Context(), 1, "", nil, errors.New("HTTP 503"), time.Unix(now+100, 0)))
			state, err := model.GetCopilotPricingState()
			require.NoError(t, err)
			assert.Equal(t, now, state.Sources[1].LastSuccessAt)
			assert.Equal(t, "HTTP 503", state.Sources[1].Error)
			assert.NoError(t, model.CheckCopilotPricing(1, alias, "future-model", now+model.CopilotPricingMaxAge-1))
			assert.ErrorContains(t, model.CheckCopilotPricing(1, alias, "future-model", now+model.CopilotPricingMaxAge), "expired")
			assert.ErrorContains(t, model.CheckCopilotPricing(1, "github-copilot/unpriced-model", "unpriced-model", now+100), "needs review")

			require.NoError(t, model.RecordCopilotPricingCheck(t.Context(), 1, strings.Repeat("2", 64), map[string]string{"future-model": changedExpression}, nil, time.Unix(now+200, 0)))
			assert.ErrorContains(t, model.CheckCopilotPricing(1, alias, "future-model", now+200), "needs review")
			snapshot, err = model.GetModelPricingSnapshot([]string{alias})
			require.NoError(t, err)
			require.Len(t, snapshot.Entries, 1)
			change.ExpectedVersion = snapshot.Entries[0].Version
			change.Pricing["billing_setting.billing_expr"] = changedExpression
			require.ErrorContains(t, model.UpdateModelPricing([]model.ModelPricingChange{change}), "needs review", "a stale source cannot approve a newer tariff")
			effective, ok := billing_setting.GetBillingExpr(alias)
			require.True(t, ok)
			assert.Equal(t, expression, effective, "a rejected approval rolls back the price write")
			reference.Version = model.CopilotPricingVersion("future-model", changedExpression)
			change.Pricing["billing_setting.billing_expr"] = expression
			require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{change}))
			assert.NoError(t, model.CheckCopilotPricing(1, alias, "future-model", now+200))
			effective, _ = billing_setting.GetBillingExpr(alias)
			assert.Equal(t, expression, effective, "review can explicitly keep administrator pricing")

			for range 2 {
				common.OptionMapRWMutex.Lock()
				delete(common.OptionMap, model.CopilotPricingStateKey)
				common.OptionMapRWMutex.Unlock()
				require.NoError(t, model.LoadOptionsFromDatabase())
				assert.NoError(t, model.CheckCopilotPricing(1, alias, "future-model", now+200), "approval and source survive reload")
			}
			require.NoError(t, model.RecordCopilotPricingCheck(t.Context(), 1, strings.Repeat("3", 64), map[string]string{"another-model": expression}, nil, time.Unix(now+250, 2)))
			assert.ErrorContains(t, model.CheckCopilotPricing(1, alias, "future-model", now+250), "no verified published price", "partial catalogs never retain a vanished model as free")
			assert.Error(t, model.RecordCopilotPricingCheck(t.Context(), 1, strings.Repeat("1", 64), map[string]string{"future-model": expression}, nil, time.Unix(now+250, 1)), "an older check cannot replace newer data even within the same second")
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			assert.Error(t, model.RecordCopilotPricingCheck(cancelled, 1, strings.Repeat("4", 64), map[string]string{"future-model": expression}, nil, time.Unix(now+260, 0)))
			state, err = model.GetCopilotPricingState()
			require.NoError(t, err)
			assert.Equal(t, strings.Repeat("3", 64), state.Sources[1].Version, "failed persistence cannot publish a fresh cache")
		})
	}
}

func TestCopilotPricingImport(t *testing.T) {
	const markdown = `All prices are **per 1 million tokens**.
| Model | Tier | Threshold (input tokens) | Input | Cached input | Cache write | Output |
| --- | --- | --- | --- | --- | --- | --- |
| Future Model | Default | ≤ 272K | $1 | $0.05 | $1.25 | $5 |
| Future Model | Long context | > 272K | $2 | $0.1 | $2.5 | $7.5 |
| Promo Model[^promo] | Default | Not applicable | $0 | $0 | $0 | $0 |
`
	models := []providerplugin.CatalogModel{{ID: "future-model", Name: "Future Model"}, {ID: "promo-model", Name: "Promo Model"}, {ID: "unpriced-model"}}
	before := billing_setting.GetBillingExprCopy()
	data, err := parseCopilotPrices(markdown, models)
	require.NoError(t, err)
	expressions := data["billing_expr"].(map[string]string)
	require.Len(t, expressions, 1)
	expression := expressions["github-copilot/future-model"]
	require.NotEmpty(t, expression)
	assert.Equal(t, before, billing_setting.GetBillingExprCopy(), "fetching is a proposal, not a pricing update")
	for _, tc := range []struct {
		prompt, want int
	}{
		{272000, 46000},
		{272001, 90751},
	} {
		usage := dto.Usage{PromptTokens: tc.prompt, CompletionTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 200000, CacheWriteTokens: 20000,
			CacheWriteTokensDetails: &dto.CacheWriteTokenDetails{TTL30mTokens: 10000, TTL12hTokens: 10000},
		}}
		params := service.BuildTieredTokenParams(&usage, false, billingexpr.UsedVars(expression))
		result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{
			ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000,
		}, params)
		require.NoError(t, err)
		assert.Equal(t, tc.want, result.ActualQuotaAfterGroup)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"changed units", "per 1 million tokens", "per 1 thousand tokens"},
		{"missing price", "$1 |", " |"},
		{"nonfinite price", "$1 |", "$NaN |"},
		{"wrong currency", "$1 |", "EUR1 |"},
		{"missing context tier", "Future Model | Long context", "Different Model | Long context"},
		{"missing threshold", "≤ 272K", ""},
		{"new price category", "Cache write", "New surcharge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCopilotPrices(strings.Replace(markdown, tc.from, tc.to, 1), models)
			require.Error(t, err)
		})
	}
}

func TestCopilotPricingLiveSource(t *testing.T) {
	if os.Getenv("COPILOT_PRICING_LIVE_TEST") != "1" {
		t.Skip("opt-in read-only check of GitHub's published pricing schema")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, copilotPricingSource, nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	require.NoError(t, err)
	data, err := parseCopilotPrices(string(body), []providerplugin.CatalogModel{{ID: "gpt-6.1-terra", Name: "GPT-6.1 Terra"}})
	require.NoError(t, err)
	expression := data["billing_expr"].(map[string]string)["github-copilot/gpt-6.1-terra"]
	_, err = billingexpr.CompileFromCache(expression)
	require.NoError(t, err)
}

func TestPricingSyncExpressionPriority(t *testing.T) {
	expression := `tier("base", p * 2 + c * 8 + cr * 0)`
	cases := []struct {
		name       string
		local      map[string]any
		source     map[string]any
		wantFields []string
	}{
		{"equal expressions suppress stale ratios", map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": expression}}, map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": expression}, "model_ratio": map[string]float64{"m": 3}, "model_price": map[string]float64{"m": 2}}, nil},
		{"local expression excludes legacy-only source", map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": expression}}, map[string]any{"model_ratio": map[string]float64{"m": 3}, "completion_ratio": map[string]float64{"m": 2}}, nil},
		{"expression imports without legacy conflicts", map[string]any{"model_ratio": map[string]float64{"m": 1}}, map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": expression}, "model_ratio": map[string]float64{"m": 3}, "model_price": map[string]float64{"m": 2}}, []string{"billing_mode", "billing_expr"}},
		{"inactive expression follows explicit ratio mode", map[string]any{"model_ratio": map[string]float64{"m": 1}}, map[string]any{"billing_mode": map[string]string{"m": "ratio"}, "billing_expr": map[string]string{"m": expression}, "model_ratio": map[string]float64{"m": 3}}, []string{"model_ratio"}},
		{"empty active expression never imports a false free price", map[string]any{}, map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": " "}, "model_ratio": map[string]float64{"m": 0}}, nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			diff := buildDifferences(tt.local, []struct {
				name string
				data map[string]any
			}{{"source", tt.source}})
			fields := make([]string, 0, len(diff["m"]))
			for field := range diff["m"] {
				fields = append(fields, field)
			}
			assert.ElementsMatch(t, tt.wantFields, fields)
		})
	}
}

func TestRatioConfigExportsEffectiveExpressions(t *testing.T) {
	before := config.GlobalConfig.ExportAllConfigs()
	expose := ratio_setting.IsExposeRatioEnabled()
	t.Cleanup(func() {
		config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"billing_mode": before["billing_setting.billing_mode"], "billing_expr": before["billing_setting.billing_expr"]})
		ratio_setting.SetExposeRatioEnabled(expose)
	})
	config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"billing_mode": `{"sync-export":"tiered_expr"}`, "billing_expr": `{"sync-export":"tier(\"base\", p * 2)"}`})
	ratio_setting.SetExposeRatioEnabled(true)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ratio_config", nil)
	GetRatioConfig(c)
	var response struct {
		Success bool
		Data    map[string]any
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, billing_setting.BillingModeTieredExpr, valueMap(response.Data["billing_mode"])["sync-export"])
	assert.Equal(t, `tier("base", p * 2)`, valueMap(response.Data["billing_expr"])["sync-export"])
}

func TestPricingSyncCompleteSourcesAndArrayFormats(t *testing.T) {
	before := config.GlobalConfig.ExportAllConfigs()
	oldRatios, oldCompletion := ratio_setting.ModelRatio2JSONString(), ratio_setting.CompletionRatio2JSONString()
	t.Cleanup(func() {
		config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"billing_mode": before["billing_setting.billing_mode"], "billing_expr": before["billing_setting.billing_expr"]})
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatios))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(oldCompletion))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"sync-token":1}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"sync-token":2}`))
	expression := `len <= 200000 ? tier("short", p * 2 + c * 8 + cr * 0) : tier("long", p * 4 + c * 12)`
	expressions, err := common.Marshal(map[string]string{"sync-already": expression})
	require.NoError(t, err)
	config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"billing_mode": `{"sync-already":"tiered_expr"}`, "billing_expr": string(expressions)})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		if r.URL.Path == "/ratio_config" {
			data = map[string]any{
				"billing_mode":     map[string]string{"sync-already": "tiered_expr", "sync-expression": "tiered_expr"},
				"billing_expr":     map[string]string{"sync-already": expression, "sync-expression": expression},
				"model_ratio":      map[string]float64{"sync-already": 9, "sync-expression": 9, "sync-token": 1},
				"model_price":      map[string]float64{"sync-expression": 4},
				"completion_ratio": map[string]float64{"sync-token": 4},
				"cache_ratio":      map[string]float64{"sync-token": 0},
			}
		} else {
			data = []map[string]any{
				{"model_name": "sync-already", "model_ratio": 5, "completion_ratio": 3},
				{"model_name": "sync-expression", "model_ratio": 2, "model_price": 1},
				{"model_name": "sync-array-expression", "billing_mode": "tiered_expr", "billing_expr": expression, "quota_type": 1, "model_price": 0},
				{"model_name": "sync-unpriced"},
				{"model_name": "sync-invalid-expression", "billing_mode": "tiered_expr", "billing_expr": "", "model_ratio": 0},
				{"model_name": "sync-free", "model_ratio": 0, "completion_ratio": 0},
			}
		}
		encoded, err := common.Marshal(map[string]any{"success": true, "data": data})
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	}))
	defer server.Close()
	var response struct {
		Success bool
		Data    struct {
			Differences map[string]map[string]dto.DifferenceItem
			Prices      map[string]struct {
				Current   map[string]any
				Upstreams map[string]map[string]any
			}
			TestResults []dto.TestResult `json:"test_results"`
		}
	}
	body := map[string]any{"upstreams": []map[string]any{
		{"id": 1, "name": "Expressions", "base_url": server.URL, "endpoint": "/ratio_config"},
		{"id": 2, "name": "Legacy", "base_url": server.URL, "endpoint": "/pricing"},
	}}
	recorder := modelManagementRequest(t, FetchUpstreamRatios, http.MethodPost, "/api/channel/fetch_upstream_ratios", body, &response)
	require.True(t, response.Success, recorder.Body.String())
	require.Len(t, response.Data.TestResults, 2)
	for _, result := range response.Data.TestResults {
		require.Equal(t, "success", result.Status, result.Error)
	}
	assert.NotContains(t, response.Data.Differences, "sync-already")
	assert.NotContains(t, response.Data.Differences, "sync-unpriced")
	assert.NotContains(t, response.Data.Differences, "sync-invalid-expression")
	assert.Equal(t, map[string]any{"billing_mode": "tiered_expr", "billing_expr": expression}, response.Data.Prices["sync-expression"].Upstreams["Expressions(1)"])
	assert.NotContains(t, response.Data.Prices["sync-expression"].Upstreams, "Legacy(2)")
	assert.Equal(t, map[string]any{"billing_mode": "tiered_expr", "billing_expr": expression}, response.Data.Prices["sync-array-expression"].Upstreams["Legacy(2)"])
	assert.Equal(t, float64(1), response.Data.Prices["sync-token"].Upstreams["Expressions(1)"]["model_ratio"], "unchanged base prices are included for a complete price preview")
	assert.Equal(t, float64(4), response.Data.Prices["sync-token"].Upstreams["Expressions(1)"]["completion_ratio"])
	assert.Equal(t, float64(0), response.Data.Prices["sync-token"].Upstreams["Expressions(1)"]["cache_ratio"])
	assert.Equal(t, float64(0), response.Data.Prices["sync-free"].Upstreams["Legacy(2)"]["model_ratio"])
	assert.NotContains(t, response.Data.Prices, "sync-already")
	body["include_unchanged"] = true
	recorder = modelManagementRequest(t, FetchUpstreamRatios, http.MethodPost, "/api/ratio_sync/fetch", body, &response)
	require.True(t, response.Success, recorder.Body.String())
	assert.Equal(t, map[string]any{"billing_mode": "tiered_expr", "billing_expr": expression}, response.Data.Prices["sync-already"].Upstreams["Expressions(1)"])
	assert.NotContains(t, response.Data.Prices["sync-already"].Upstreams, "Legacy(2)")
	assert.NotContains(t, response.Data.Prices, "sync-unpriced")
	assert.NotContains(t, response.Data.Prices, "sync-invalid-expression")
	assert.NotContains(t, response.Data.Differences, "sync-already")
}
