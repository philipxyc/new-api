package billing_setting_test

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubCopilotBuiltinBilling(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		*settings = saved
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-6.1-terra":7}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))

	for _, tc := range []struct {
		name  string
		usage dto.Usage
		quota int
	}{
		{"claude-haiku-4.5", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 750},
		{"claude-sonnet-5", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 1500},
		{"claude-haiku-5.5", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 75},
		{"claude-sonnet-5.5", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 1500},
		{"claude-opus-5.5", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 3000},
		{"gpt-5.3-codex", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 1575},
		{"gpt-5.4-nano", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 163},
		{"gpt-5.6-luna", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 160},
		{"gpt-5.6-sol", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 3000},
		{"gpt-5.6-terra", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 1600},
		{"gpt-6.1-terra", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 750},
		{"gpt-6.1-sol", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 1500},
		{"gpt-6-astra", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 7500},
		{"gpt-6-luna", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 75},
		{"gpt-6-sol", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 1500},
		{"grok-4.6", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 1300},
		{"grok-4.7", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 1300},
		{"mai-code-1.1-flash", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 160},
		{"gemini-4-hydrogen", dto.Usage{PromptTokens: 1000, CompletionTokens: 100}, 1125},
		{"claude-haiku-5.5", dto.Usage{PromptTokens: 100000, CompletionTokens: 1000}, 5250},
		{"claude-haiku-5.5", dto.Usage{PromptTokens: 100001, CompletionTokens: 1000}, 26250},
		{"grok-4.7", dto.Usage{PromptTokens: 200000, CompletionTokens: 1000}, 203000},
		{"grok-4.7", dto.Usage{PromptTokens: 200001, CompletionTokens: 1000}, 406002},
		{"gpt-6.1-terra", dto.Usage{PromptTokens: 272000, CompletionTokens: 1000,
			PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 200000, CacheWriteTokens: 20000}}, 46000},
		{"gpt-6.1-terra", dto.Usage{PromptTokens: 272001, CompletionTokens: 1000,
			PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 200000, CacheWriteTokens: 20000}}, 90751},
		{"gpt-5.6-luna", dto.Usage{PromptTokens: 200000, CompletionTokens: 1000,
			PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 150000, CacheWriteTokens: 10000}}, 7350},
		{"gpt-5.6-luna", dto.Usage{PromptTokens: 200001, CompletionTokens: 1000,
			PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 150000, CacheWriteTokens: 10000}}, 14400},
		{"gpt-6.1-terra", dto.Usage{PromptTokens: 12000, PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 4000, CacheWriteTokens: 7000,
			CacheWriteTokensDetails: &dto.CacheWriteTokenDetails{TTL30mTokens: 3000, TTL12hTokens: 4000}}}, 4975},
		{"claude-sonnet-5", dto.Usage{PromptTokens: 1000, CompletionTokens: 100, UsageSemantic: "anthropic",
			ClaudeCacheCreation5mTokens: 100, ClaudeCacheCreation1hTokens: 200,
			PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 2000, CachedCreationTokens: 300}}, 2075},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "github-copilot/" + tc.name
			expression, ok := billing_setting.GetBillingExpr(name)
			require.True(t, ok)
			params := service.BuildTieredTokenParams(&tc.usage, tc.usage.UsageSemantic == "anthropic", billingexpr.UsedVars(expression))
			result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{
				ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000,
			}, params)
			require.NoError(t, err)
			assert.Equal(t, tc.quota, result.ActualQuotaAfterGroup)
		})
	}
	assert.Equal(t, billing_setting.BillingModeRatio, billing_setting.GetBillingMode("gpt-6.1-terra"), "direct-provider pricing is unchanged")
	settings.BillingMode["github-copilot/gpt-6.1-terra"] = billing_setting.BillingModeTieredExpr
	settings.BillingExpr["github-copilot/gpt-6.1-terra"] = `tier("custom", p * 7)`
	expression, ok := billing_setting.GetBillingExpr("github-copilot/gpt-6.1-terra")
	require.True(t, ok)
	result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{
		ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 2, QuotaPerUnit: 500000,
	}, billingexpr.TokenParams{P: 1000, Len: 1000})
	require.NoError(t, err)
	assert.Equal(t, 7000, result.ActualQuotaAfterGroup, "administrator overrides and group multipliers remain effective")
}

func TestGPT6AstraBuiltinBilling(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	savedOptions := common.OptionMap
	t.Cleanup(func() {
		*settings, common.OptionMap = saved, savedOptions
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	common.OptionMap = map[string]string{"billing_setting.billing_mode": `{}`, "billing_setting.billing_expr": `{}`}
	require.NoError(t, config.GlobalConfig.LoadFromDB(common.OptionMap))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	assert.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode("gpt-6-astra"))
	expression, ok := billing_setting.GetBillingExpr("gpt-6-astra")
	require.True(t, ok)

	for _, tc := range []struct {
		name                                       string
		input, output, cached, written, written12h int
		request                                    string
		quota                                      int
	}{
		{"standard", 1000, 100, 0, 0, 0, `{}`, 7500},
		{"client flex cannot discount standard pricing", 1000, 100, 0, 0, 0, `{"service_tier":"flex"}`, 7500},
		{"cache at context boundary", 272000, 1000, 200000, 20000, 0, `{}`, 510000},
		{"whole request above boundary", 272001, 1000, 200000, 20000, 0, `{}`, 1007510},
		// The prompt caching guide example: 4000 of the 7000 writes are 12h.
		{"12h cache writes", 12000, 0, 4000, 7000, 4000, `{}`, 75750},
		{"12h cache writes above boundary", 300000, 0, 200000, 20000, 10000, `{}`, 1375000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := &dto.Usage{
				PromptTokens: tc.input, CompletionTokens: tc.output,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: tc.cached, CacheWriteTokens: tc.written,
					CacheWriteTokensDetails: &dto.CacheWriteTokenDetails{TTL30mTokens: tc.written - tc.written12h, TTL12hTokens: tc.written12h}},
			}
			params := service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression))
			result, err := billingexpr.ComputeTieredQuotaWithRequest(&billingexpr.BillingSnapshot{
				ExprString: expression, GroupRatio: 1, QuotaPerUnit: 500000,
			}, params, billingexpr.RequestInput{Body: []byte(tc.request)})
			require.NoError(t, err)
			assert.Equal(t, tc.quota, result.ActualQuotaAfterGroup)
		})
	}

	t.Run("admin options expose defaults without persisting them", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		controller.GetOptions(ctx)
		var response struct {
			Success bool
			Data    []model.Option
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.True(t, response.Success)
		found := map[string]string{}
		for _, option := range response.Data {
			if _, ok := common.OptionMap[option.Key]; ok {
				assert.NotContains(t, found, option.Key)
				var values map[string]string
				require.NoError(t, common.UnmarshalJsonStr(option.Value, &values))
				found[option.Key] = values["gpt-6-astra"]
				assert.Equal(t, `{}`, common.OptionMap[option.Key])
			}
		}
		assert.Equal(t, map[string]string{"billing_setting.billing_mode": "tiered_expr", "billing_setting.billing_expr": expression}, found)
	})

	for _, tc := range []struct {
		name, mode, expr, ratios, prices, wantMode string
	}{
		{"custom expression overrides legacy price", "tiered_expr", "p * 7", `{"gpt-6-astra":8}`, `{}`, "tiered_expr"},
		{"explicit ratio mode", "ratio", "", `{}`, `{}`, "ratio"},
		{"existing free token price", "", "", `{"gpt-6-astra":0}`, `{}`, "ratio"},
		{"existing per-call price", "", "", `{}`, `{"gpt-6-astra":0.1}`, "ratio"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
			if tc.mode != "" {
				settings.BillingMode["gpt-6-astra"] = tc.mode
			}
			if tc.expr != "" {
				settings.BillingExpr["gpt-6-astra"] = tc.expr
			}
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(tc.ratios))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(tc.prices))
			assert.Equal(t, tc.wantMode, billing_setting.GetBillingMode("gpt-6-astra"))
			actual, ok := billing_setting.GetBillingExpr("gpt-6-astra")
			assert.Equal(t, tc.expr, actual)
			assert.Equal(t, tc.expr != "", ok)
		})
	}
}

func TestImageModelBuiltinPricesAndOverrides(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		*settings = saved
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	for _, name := range []string{"gpt-image-2", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare"} {
		t.Run(name, func(t *testing.T) {
			*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
			expression, ok := billing_setting.GetBillingExpr(name)
			require.True(t, ok)
			usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 100, PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens: 300, ImageTokens: 600, CachedTokensDetails: &dto.CachedTokenDetails{ImageTokens: common.GetPointer(200)},
			}}
			result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000},
				service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression)))
			require.NoError(t, err)
			assert.Equal(t, 4113, result.ActualQuotaAfterGroup)
			encoded, err := common.Marshal(map[string]float64{name: 0})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(encoded)))
			assert.Equal(t, "ratio", billing_setting.GetBillingMode(name))
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(encoded)))
			assert.Equal(t, "ratio", billing_setting.GetBillingMode(name))
			settings.BillingMode[name] = "tiered_expr"
			settings.BillingExpr[name] = `tier("custom", p * 7)`
			actual, ok := billing_setting.GetBillingExpr(name)
			require.True(t, ok)
			assert.Equal(t, settings.BillingExpr[name], actual)
		})
	}
}

// DeepSeek prices are peak/off-peak expressions in USD per 1M tokens; peak is
// twice off-peak. The same usage settles to the same quota from a Chat,
// Responses or Anthropic response, and the Search/Fetch routing names cost
// $0.75 per 1,000 requests with no token price. Which tier runs depends on the clock; both are pinned here.
func TestDeepSeekBuiltinBilling(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		*settings = saved
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))

	// 1000 cache-miss + 2000 cache-hit input tokens and 500 output tokens.
	chat := &dto.Usage{PromptTokens: 3000, CompletionTokens: 500, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 2000}}
	responses := &dto.Usage{}
	service.ApplyResponsesUsage(responses, &dto.Usage{InputTokens: 3000, OutputTokens: 500, InputTokensDetails: &dto.InputTokenDetails{CachedTokens: 2000}})
	anthropic := &dto.Usage{PromptTokens: 1000, CompletionTokens: 500, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 2000}, UsageSemantic: "anthropic"}
	quota := func(expression string, usage *dto.Usage) billingexpr.TieredResult {
		params := service.BuildTieredTokenParams(usage, usage.UsageSemantic == "anthropic", billingexpr.UsedVars(expression))
		result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{
			ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000,
		}, params)
		require.NoError(t, err)
		return result
	}
	for model, want := range map[string]map[string]int{
		// flash: 1000*0.15 + 2000*0.003 + 500*0.6 = $0.000456 off-peak.
		"deepseek-flash":               {"off_peak": 228, "peak": 456},
		"deepseek-v4-flash":            {"off_peak": 228, "peak": 456},
		"deepseek-v4-flash-vision-exp": {"off_peak": 228, "peak": 456},
		// pro: 1000*0.66 + 2000*0.022 + 500*1.98 = $0.001694 off-peak.
		"deepseek-v4-pro": {"off_peak": 847, "peak": 1694},
	} {
		t.Run(model, func(t *testing.T) {
			assert.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode(model))
			expression, ok := billing_setting.GetBillingExpr(model)
			require.True(t, ok)
			for name, usage := range map[string]*dto.Usage{"chat": chat, "responses": responses, "anthropic": anthropic} {
				result := quota(expression, usage)
				require.Contains(t, want, result.MatchedTier, name)
				assert.Equal(t, want[result.MatchedTier], result.ActualQuotaAfterGroup, name)
			}
		})
	}
	for _, model := range []string{"deepseek-search", "deepseek-fetch"} {
		expression, ok := billing_setting.GetBillingExpr(model)
		require.True(t, ok, model)
		result := quota(expression, &dto.Usage{})
		assert.Equal(t, billingexpr.BillingUnitRequest, result.BillingUnit, model)
		assert.Equal(t, 375, result.ActualQuotaAfterGroup, model)
	}
}
