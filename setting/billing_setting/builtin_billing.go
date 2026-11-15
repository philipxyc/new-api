package billing_setting

import "github.com/QuantumNous/new-api/constant"

// deepSeekPeak is DeepSeek's peak window: Monday to Friday, 09:00-12:00 and
// 14:00-18:00 Beijing time. The Chinese public holidays DeepSeek bills
// off-peak are not known in advance and bill at peak here.
const deepSeekPeak = `weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 && ((hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12) || (hour("Asia/Shanghai") >= 14 && hour("Asia/Shanghai") < 18))`

const deepSeekFlashExpr = deepSeekPeak + ` ? tier("peak", p * 0.3 + cr * 0.006 + c * 1.2) : tier("off_peak", p * 0.15 + cr * 0.003 + c * 0.6)`

// Built-in token prices use actual USD per million tokens. Keep new model
// defaults here instead of splitting them across the legacy ratio tables.
var builtinBillingExpr = map[string]string{
	// https://docs.github.com/en/copilot/reference/copilot-billing/models-and-pricing
	// Verified 2026-11-15 against entitled model IDs. Use channel model_mapping
	// from these client aliases to the bare upstream IDs: other providers'
	// prices must not change. GitHub publishes one cache-write rate, not TTL
	// multipliers. Unlisted models require an explicit administrator price.
	"github-copilot/claude-haiku-4.5":   `tier("standard", p * 1 + cr * 0.1 + cc * 1.25 + cc1h * 1.25 + c * 5)`,
	"github-copilot/claude-sonnet-5":    `tier("standard", p * 2 + cr * 0.2 + cc * 2.5 + cc1h * 2.5 + c * 10)`,
	"github-copilot/claude-haiku-5.5":   `len <= 100000 ? tier("standard", p * 0.1 + cr * 0.01 + cc * 0.125 + cc1h * 0.125 + c * 0.5) : tier("long_context", p * 0.5 + cr * 0.05 + cc * 0.625 + cc1h * 0.625 + c * 2.5)`,
	"github-copilot/claude-sonnet-5.5":  `tier("standard", p * 2 + cr * 0.1 + cc * 2.5 + cc1h * 2.5 + c * 10)`,
	"github-copilot/claude-opus-5.5":    `tier("standard", p * 4 + cr * 0.2 + cc * 5 + cc1h * 5 + c * 20)`,
	"github-copilot/gpt-5.3-codex":      `tier("standard", p * 1.75 + cr * 0.175 + c * 14)`,
	"github-copilot/gpt-5.4-nano":       `tier("standard", p * 0.2 + cr * 0.02 + c * 1.25)`,
	"github-copilot/gpt-5.6-luna":       `len <= 200000 ? tier("standard", p * 0.2 + cr * 0.02 + cc * 0.25 + cc12h * 0.25 + c * 1.2) : tier("long_context", p * 0.4 + cr * 0.04 + cc * 0.5 + cc12h * 0.5 + c * 1.8)`,
	"github-copilot/gpt-5.6-sol":        `len <= 272000 ? tier("standard", p * 4 + cr * 0.4 + cc * 5 + cc12h * 5 + c * 20) : tier("long_context", p * 8 + cr * 0.8 + cc * 10 + cc12h * 10 + c * 30)`,
	"github-copilot/gpt-5.6-terra":      `len <= 272000 ? tier("standard", p * 2 + cr * 0.2 + cc * 2.5 + cc12h * 2.5 + c * 12) : tier("long_context", p * 4 + cr * 0.4 + cc * 5 + cc12h * 5 + c * 18)`,
	"github-copilot/gpt-6.1-terra":      `len <= 272000 ? tier("standard", p * 1 + cr * 0.05 + cc * 1.25 + cc12h * 1.25 + c * 5) : tier("long_context", p * 2 + cr * 0.1 + cc * 2.5 + cc12h * 2.5 + c * 7.5)`,
	"github-copilot/gpt-6.1-sol":        `len <= 272000 ? tier("standard", p * 2 + cr * 0.1 + cc * 2.5 + cc12h * 2.5 + c * 10) : tier("long_context", p * 4 + cr * 0.2 + cc * 5 + cc12h * 5 + c * 15)`,
	"github-copilot/gpt-6-astra":        `len <= 272000 ? tier("standard", p * 10 + cr * 1 + cc * 12.5 + cc12h * 12.5 + c * 50) : tier("long_context", p * 20 + cr * 2 + cc * 25 + cc12h * 25 + c * 75)`,
	"github-copilot/gpt-6-luna":         `len <= 272000 ? tier("standard", p * 0.1 + cr * 0.01 + cc * 0.125 + cc12h * 0.125 + c * 0.5) : tier("long_context", p * 0.2 + cr * 0.02 + cc * 0.25 + cc12h * 0.25 + c * 0.75)`,
	"github-copilot/gpt-6-sol":          `len <= 272000 ? tier("standard", p * 2 + cr * 0.2 + cc * 2.5 + cc12h * 2.5 + c * 10) : tier("long_context", p * 4 + cr * 0.4 + cc * 5 + cc12h * 5 + c * 15)`,
	"github-copilot/grok-4.6":           `len <= 200000 ? tier("standard", p * 2 + cr * 0.5 + c * 6) : tier("long_context", p * 4 + cr * 1 + c * 12)`,
	"github-copilot/grok-4.7":           `len <= 200000 ? tier("standard", p * 2 + cr * 0.5 + c * 6) : tier("long_context", p * 4 + cr * 1 + c * 12)`,
	"github-copilot/mai-code-1.1-flash": `tier("standard", p * 0.2 + cr * 0.02 + c * 1.2)`,
	"github-copilot/gemini-4-hydrogen":  `tier("standard", p * 1.5 + cr * 0.075 + c * 7.5)`,
	// https://developers.openai.com/api/docs/pricing (Standard, 2026-09-09).
	// The Images API reports image output in output_tokens, normalized to c.
	"gpt-image-2":            `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"gpt-image-2.5-sunburst": `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"gpt-image-2.5-flare":    `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	// https://developers.openai.com/api/docs/models/gpt-6-astra
	// https://developers.openai.com/api/docs/pricing (Standard, 2026-10-22):
	// cc is the 30m cache write and cc12h the 12h write. The long-context
	// rates apply to the whole request.
	// Do not infer service-tier discounts from incoming request parameters:
	// channels filter service_tier by default, so it may not reach the upstream.
	"gpt-6-astra": `len <= 272000 ? tier("standard", p * 10 + c * 50 + cr * 1 + cc * 12.5 + cc12h * 25) : tier("long_context", p * 20 + c * 75 + cr * 2 + cc * 25 + cc12h * 50)`,
	// https://api-docs.deepseek.com/quick_start/pricing (2026-10-17). Off-peak
	// is half the peak price. deepseek-v4-flash and deepseek-v4-flash-vision-exp
	// run on V4.1 Flash and bill at the Flash price. Images bill as input
	// tokens, and there is no cache-write price.
	"deepseek-flash":               deepSeekFlashExpr,
	"deepseek-v4-flash":            deepSeekFlashExpr,
	"deepseek-v4-flash-vision-exp": deepSeekFlashExpr,
	"deepseek-v4-pro":              deepSeekPeak + ` ? tier("peak", p * 1.32 + cr * 0.044 + c * 3.96) : tier("off_peak", p * 0.66 + cr * 0.022 + c * 1.98)`,
	// The Search and Fetch API routing names: $0.75 per 1,000 successful
	// requests each, the same pricing page. The calls carry no tokens.
	constant.DeepSeekSearchModel: `tier("request", fixed(0.00075))`,
	constant.DeepSeekFetchModel:  `tier("request", fixed(0.00075))`,
}
