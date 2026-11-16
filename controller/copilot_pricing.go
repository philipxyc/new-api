package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay/channel/providerplugin"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/gin-gonic/gin"
)

const copilotPricingSource = "https://docs.github.com/api/article/body?pathname=/en/copilot/reference/copilot-billing/models-and-pricing"

func isCopilotPricingSource(upstream dto.UpstreamDTO) bool {
	if upstream.Endpoint != "" && upstream.Endpoint != defaultEndpoint {
		return false
	}
	switch upstream.BaseURL {
	case "https://api.githubcopilot.com", "https://api.business.githubcopilot.com", "https://api.enterprise.githubcopilot.com":
		return true
	}
	return false
}

// fetchCopilotPrices only proposes prices. The existing synchronization review
// and version-checked save remain the sole way to change administrator pricing.
func fetchCopilotPrices(ctx context.Context, upstream dto.UpstreamDTO) (map[string]any, error) {
	channel, err := model.GetChannelById(upstream.ID, true)
	if err != nil {
		return nil, fmt.Errorf("select a saved GitHub Copilot channel")
	}
	plugin, ok := service.PluginOfChannel(channel)
	if !ok || plugin.Meta.Key != "github-copilot" || strings.TrimRight(channel.GetBaseURL(), "/") != upstream.BaseURL {
		return nil, fmt.Errorf("pricing source does not match the saved GitHub Copilot channel")
	}
	started := time.Now()
	fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	version, data, fetchErr := fetchCopilotPricingSnapshot(fetchCtx, channel, plugin)
	cancel()
	expressions := map[string]string{}
	references := map[string]model.CopilotPricingReference{}
	if fetchErr == nil {
		for alias, expression := range data[billing_setting.BillingExprField].(map[string]string) {
			id := strings.TrimPrefix(alias, "github-copilot/")
			expressions[id] = expression
			references[alias] = model.CopilotPricingReference{
				ChannelID: channel.Id, Model: id, Version: model.CopilotPricingVersion(id, expression),
			}
		}
	}
	if err := model.RecordCopilotPricingCheck(ctx, channel.Id, version, expressions, fetchErr, started); err != nil {
		return nil, err
	}
	if fetchErr != nil {
		return nil, fetchErr
	}
	data["copilot_sources"] = references
	return data, nil
}

func fetchCopilotPricingSnapshot(ctx context.Context, channel *model.Channel, plugin *jsplugin.LoadedPlugin) (string, map[string]any, error) {
	body, err := fetchProviderPluginModelList(ctx, channel, plugin)
	if err != nil {
		return "", nil, fmt.Errorf("GitHub Copilot model catalog could not be fetched")
	}
	var catalog providerplugin.ModelCatalog
	if err := common.Unmarshal(body, &catalog); err != nil {
		return "", nil, fmt.Errorf("invalid Copilot model catalog")
	}
	client, err := providerplugin.VerifiedManagementHTTPClient(channel.GetSetting().Proxy)
	if err != nil {
		return "", nil, fmt.Errorf("GitHub pricing proxy configuration is invalid")
	}
	noRedirects := *client
	noRedirects.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotPricingSource, nil)
	if err != nil {
		return "", nil, err
	}
	response, err := noRedirects.Do(request)
	if err != nil {
		return "", nil, fmt.Errorf("GitHub published pricing request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("GitHub published pricing returned HTTP %d", response.StatusCode)
	}
	markdown, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return "", nil, fmt.Errorf("GitHub published pricing response could not be read")
	}
	if len(markdown) > 1<<20 {
		return "", nil, fmt.Errorf("GitHub published pricing exceeds 1 MiB")
	}
	data, err := parseCopilotPrices(string(markdown), catalog.Data)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(markdown)
	return hex.EncodeToString(sum[:]), data, nil
}

type copilotPricingStatus struct {
	ChannelID     int      `json:"channel_id"`
	SourceURL     string   `json:"source_url"`
	Version       string   `json:"version"`
	LastAttemptAt int64    `json:"last_attempt_at"`
	LastSuccessAt int64    `json:"last_success_at"`
	ExpiresAt     int64    `json:"expires_at"`
	Error         string   `json:"error"`
	BlockedModels []string `json:"blocked_models"`
}

func GetCopilotPricingStatus(c *gin.Context) {
	status, err := copilotPricingStatuses()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, status)
}

func copilotPricingStatuses() ([]copilotPricingStatus, error) {
	state, err := model.GetCopilotPricingState()
	if err != nil {
		return nil, err
	}
	var channels []*model.Channel
	if err := model.DB.Select("id", "models", "model_mapping", "setting", "type").
		Where("type = ?", 61).Find(&channels).Error; err != nil {
		return nil, err
	}
	statuses := make([]copilotPricingStatus, 0)
	for _, channel := range channels {
		plugin, ok := service.PluginOfChannel(channel)
		if !ok || plugin.Meta.Key != "github-copilot" {
			continue
		}
		source := state.Sources[channel.Id]
		status := copilotPricingStatus{
			ChannelID: channel.Id, SourceURL: model.CopilotPricingURL, Version: source.Version,
			LastAttemptAt: source.LastAttemptAt, LastSuccessAt: source.LastSuccessAt,
			Error: source.Error, BlockedModels: []string{},
		}
		if source.LastSuccessAt > 0 {
			status.ExpiresAt = source.LastSuccessAt + model.CopilotPricingMaxAge
		}
		mapping := normalizeChannelModelMapping(channel)
		for _, name := range channel.GetModels() {
			upstream := name
			if mapped := mapping[name]; mapped != "" {
				upstream = mapped
			}
			if err := model.CheckCopilotPricing(channel.Id, name, upstream, common.GetTimestamp()); err != nil {
				status.BlockedModels = append(status.BlockedModels, name)
			}
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

type copilotPricingHandler struct{}

func (copilotPricingHandler) Type() string            { return "copilot_pricing" }
func (copilotPricingHandler) Enabled() bool           { return true }
func (copilotPricingHandler) Interval() time.Duration { return time.Hour }
func (copilotPricingHandler) NewPayload() any         { return nil }
func (copilotPricingHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	before, err := copilotPricingStatuses()
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	var channels []*model.Channel
	if err := model.DB.WithContext(ctx).Where("type = ?", 61).Find(&channels).Error; err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	var failures int
	for _, channel := range channels {
		plugin, ok := service.PluginOfChannel(channel)
		if !ok || plugin.Meta.Key != "github-copilot" {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := fetchCopilotPrices(checkCtx, dto.UpstreamDTO{ID: channel.Id, BaseURL: strings.TrimRight(channel.GetBaseURL(), "/")})
		cancel()
		if err != nil {
			failures++
			common.SysError(common.LogText("GitHub Copilot pricing check failed for channel %d: %s", channel.Id, err.Error()))
		}
		if ctx.Err() != nil {
			finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, ctx.Err())
			return
		}
	}
	statuses, err := copilotPricingStatuses()
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	status := model.SystemTaskStatusSucceeded
	blocked := 0
	attentionChanged := false
	for _, current := range statuses {
		blocked += len(current.BlockedModels)
		old := copilotPricingStatus{}
		for _, previous := range before {
			if previous.ChannelID == current.ChannelID {
				old = previous
				break
			}
		}
		if current.Error != old.Error || !slices.Equal(current.BlockedModels, old.BlockedModels) {
			attentionChanged = true
		}
		if len(current.BlockedModels) > 0 &&
			(current.Version != old.Version || (old.LastAttemptAt < current.ExpiresAt && current.LastAttemptAt >= current.ExpiresAt)) {
			attentionChanged = true
		}
	}
	if failures > 0 || blocked > 0 {
		status = model.SystemTaskStatusFailed
		err = fmt.Errorf("GitHub Copilot pricing needs attention: %d failed checks, %d blocked models", failures, blocked)
		common.SysError(common.LogText("GitHub Copilot pricing needs attention: %d failed checks, %d blocked models", failures, blocked))
		if attentionChanged {
			service.NotifyChannelWatchers(dto.NotifyTypeChannelUpdate, func(lang string) (string, string) {
				return i18n.Translate(lang, "copilot.pricing_attention_subject"),
					i18n.Translate(lang, "copilot.pricing_attention_body", map[string]any{"Failed": failures, "Blocked": blocked})
			})
		}
	}
	finishSystemTaskHandler(task, runnerID, status, statuses, err)
}

type copilotPriceTier struct {
	price     string
	threshold int64
	long      bool
}

var copilotThreshold = regexp.MustCompile(`^(\x{2264}|>)\s*([0-9]+)([KM]?)$`)
var copilotModelPunctuation = strings.NewReplacer("-", "", " ", "")

func copilotPriceIdentity(name string) string {
	return copilotModelPunctuation.Replace(strings.ToLower(strings.TrimSpace(name)))
}

func parseCopilotPrices(markdown string, models []providerplugin.CatalogModel) (map[string]any, error) {
	if !strings.Contains(markdown, "All prices are **per 1 million tokens**.") {
		return nil, fmt.Errorf("GitHub pricing units changed; review the published pricing manually")
	}
	identities := make(map[string]string, len(models))
	for _, model := range models {
		name := model.Name
		if name == "" {
			name = model.ID
		}
		identity := copilotPriceIdentity(name)
		if previous, found := identities[identity]; found && previous != model.ID {
			return nil, fmt.Errorf("Copilot catalog has ambiguous pricing names")
		}
		identities[identity] = model.ID
	}
	tiers := make(map[string][]copilotPriceTier)
	var columns []string
	for line := range strings.SplitSeq(markdown, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
			columns = nil
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if cells[0] == "Model" {
			columns = cells
			continue
		}
		if len(columns) == 0 || cells[0] == "" || strings.Trim(cells[0], "-:") == "" {
			continue
		}
		// Footnoted/promotional rates are not a durable pricing policy.
		if strings.ContainsAny(cells[0], "[*") {
			continue
		}
		id, entitled := identities[copilotPriceIdentity(cells[0])]
		if !entitled {
			continue
		}
		if len(cells) != len(columns) {
			return nil, fmt.Errorf("GitHub pricing table changed for %s", id)
		}
		row := make(map[string]string, len(columns))
		for i, column := range columns {
			switch column {
			case "Model", "Release status", "Category", "Tier", "Threshold (input tokens)", "Input", "Cached input", "Cache write", "Output":
			default:
				return nil, fmt.Errorf("unsupported GitHub pricing column %q for %s", column, id)
			}
			if _, duplicate := row[column]; duplicate {
				return nil, fmt.Errorf("duplicate GitHub pricing column for %s", id)
			}
			row[column] = cells[i]
		}
		tier, err := parseCopilotPriceTier(row)
		if err != nil {
			return nil, fmt.Errorf("GitHub pricing for %s: %w", id, err)
		}
		tiers[id] = append(tiers[id], tier)
	}
	modes, expressions := map[string]string{}, map[string]string{}
	for id, prices := range tiers {
		var expression string
		switch {
		case len(prices) == 1 && prices[0].threshold == 0 && !prices[0].long:
			expression = `tier("standard", ` + prices[0].price + `)`
		case len(prices) == 2:
			standard, extended := prices[0], prices[1]
			if standard.long {
				standard, extended = extended, standard
			}
			if standard.long || !extended.long || standard.threshold <= 0 || standard.threshold != extended.threshold {
				return nil, fmt.Errorf("inconsistent GitHub context tiers for %s", id)
			}
			expression = fmt.Sprintf(`len <= %d ? tier("standard", %s) : tier("long_context", %s)`, standard.threshold, standard.price, extended.price)
		default:
			return nil, fmt.Errorf("incomplete or unsupported GitHub context tiers for %s", id)
		}
		alias := "github-copilot/" + id
		if err := billing_setting.SmokeTestExpr(expression); err != nil {
			return nil, fmt.Errorf("GitHub pricing expression is invalid for %s", id)
		}
		modes[alias], expressions[alias] = billing_setting.BillingModeTieredExpr, expression
	}
	if len(expressions) == 0 {
		return nil, fmt.Errorf("no unambiguous, non-promotional published prices matched the Copilot catalog")
	}
	return map[string]any{billing_setting.BillingModeField: modes, billing_setting.BillingExprField: expressions}, nil
}

func parseCopilotPriceTier(row map[string]string) (copilotPriceTier, error) {
	var tier copilotPriceTier
	var terms []string
	for _, field := range []struct{ column, variable string }{{"Input", "p"}, {"Cached input", "cr"}, {"Cache write", "cc"}, {"Output", "c"}} {
		value, present := row[field.column]
		if field.column == "Cache write" && (!present || value == "Not applicable") {
			continue
		}
		number, dollar := strings.CutPrefix(value, "$")
		price, err := strconv.ParseFloat(number, 64)
		if !dollar || err != nil || !isValidNonNegativeCost(price) {
			return tier, fmt.Errorf("invalid or missing %s USD price", field.column)
		}
		amount := strconv.FormatFloat(price, 'f', -1, 64)
		terms = append(terms, field.variable+" * "+amount)
		if field.variable == "cc" {
			terms = append(terms, "cc1h * "+amount, "cc12h * "+amount)
		}
	}
	tier.price = strings.Join(terms, " + ")
	threshold, hasThreshold := row["Threshold (input tokens)"]
	label, hasTier := row["Tier"]
	if hasThreshold != hasTier || (hasThreshold && threshold == "") {
		return tier, fmt.Errorf("missing context tier or threshold")
	}
	if !hasThreshold || threshold == "Not applicable" {
		if label != "" && label != "Default" {
			return tier, fmt.Errorf("unsupported pricing tier %q", label)
		}
		return tier, nil
	}
	match := copilotThreshold.FindStringSubmatch(threshold)
	if match == nil {
		return tier, fmt.Errorf("unsupported context threshold %q", threshold)
	}
	count, err := strconv.ParseInt(match[2], 10, 32)
	if err != nil || count <= 0 {
		return tier, fmt.Errorf("invalid context threshold")
	}
	switch match[3] {
	case "K":
		count *= 1000
	case "M":
		count *= 1000000
	}
	tier.threshold, tier.long = count, match[1] == ">"
	if (!tier.long && label != "Default") || (tier.long && label != "Long context") {
		return tier, fmt.Errorf("inconsistent context tier label")
	}
	return tier, nil
}
