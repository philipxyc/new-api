package providerplugin

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/samber/hot"
	"golang.org/x/sync/singleflight"
)

// CatalogModel is a provider's discovered model, not an enabled channel model.
// Routes may select only paths the plugin's manifest already declares.
type CatalogModel struct {
	ID                 string                    `json:"id"`
	Name               string                    `json:"name,omitempty"`
	Routes             []dto.AdvancedCustomRoute `json:"routes"`
	ResponsesWebSocket bool                      `json:"responses_websocket,omitempty"`
}

type ModelCatalog struct {
	Data []CatalogModel `json:"data"`
}

type catalogEntry struct {
	catalog *ModelCatalog
	err     error
}

type catalogKey struct {
	plugin *jsplugin.LoadedPlugin
	scope  [32]byte
}

var modelCatalogs = hot.NewHotCache[catalogKey, catalogEntry](hot.LRU, 128).WithTTL(5 * time.Minute).Build()
var catalogFetches singleflight.Group

// FetchModelCatalog reuses the provider's authenticated model-list transport.
// A manual refresh replaces the cache; failures never fall back to old routes.
func FetchModelCatalog(ctx context.Context, plugin *jsplugin.LoadedPlugin, info *relaycommon.RelayInfo, client ManagementClient, refresh bool) (*ModelCatalog, error) {
	encoded, err := common.Marshal([]any{info.ChannelId, info.ApiKey, info.ChannelBaseUrl, info.ChannelSetting.Proxy, info.ChannelOtherSettings.PluginConfig, info.HeadersOverride})
	if err != nil {
		return nil, err
	}
	key := catalogKey{plugin: plugin, scope: sha256.Sum256(encoded)}
	if !refresh {
		entry, found, err := modelCatalogs.Get(key)
		if err != nil {
			return nil, err
		}
		if found {
			return entry.catalog, entry.err
		}
	}
	type fetchedCatalog struct {
		catalog *ModelCatalog
		key     string
	}
	// The loader owns its request snapshot; one cancelled caller must not
	// cancel the shared fetch or mutate another caller's channel credentials.
	snapshot := *info
	meta := *info.ChannelMeta
	snapshot.ChannelMeta = &meta
	snapshot.ChannelOtherSettings.AdvancedCustom = plugin.Meta.ChannelRelayConfig(plugin.ChannelConfigFor(snapshot.ChannelOtherSettings.PluginConfig))
	flight := catalogFetches.DoChan(fmt.Sprintf("%p:%x", plugin, key.scope), func() (any, error) {
		if !refresh {
			entry, found, err := modelCatalogs.Get(key)
			if err != nil {
				return nil, err
			}
			if found {
				return fetchedCatalog{catalog: entry.catalog, key: snapshot.ApiKey}, entry.err
			}
		}
		fetchCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		body, err := FetchModelList(fetchCtx, plugin, &snapshot, client)
		var catalog *ModelCatalog
		if err == nil {
			catalog, err = parseModelCatalog(plugin, body)
		}
		entry := catalogEntry{catalog: catalog, err: err}
		if err != nil {
			modelCatalogs.SetWithTTL(key, entry, 5*time.Second)
			return nil, err
		}
		modelCatalogs.Set(key, entry)
		return fetchedCatalog{catalog: catalog, key: snapshot.ApiKey}, nil
	})
	select {
	case result := <-flight:
		if result.Err != nil {
			return nil, result.Err
		}
		fetched := result.Val.(fetchedCatalog)
		info.ApiKey = fetched.key
		return fetched.catalog, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func parseModelCatalog(plugin *jsplugin.LoadedPlugin, body []byte) (*ModelCatalog, error) {
	if len(body) > 1<<20 {
		return nil, fmt.Errorf("provider model catalog exceeds 1 MiB")
	}
	var catalog ModelCatalog
	if err := common.Unmarshal(body, &catalog); err != nil {
		return nil, fmt.Errorf("provider returned an invalid model catalog")
	}
	if catalog.Data == nil || len(catalog.Data) > 1024 {
		return nil, fmt.Errorf("provider model catalog must contain at most 1024 models")
	}
	seen := make(map[string]bool, len(catalog.Data))
	for _, model := range catalog.Data {
		if model.ID == "" || len(model.ID) > 128 || seen[model.ID] || strings.ContainsFunc(model.ID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return nil, fmt.Errorf("provider model catalog has an invalid or duplicate model ID")
		}
		seen[model.ID] = true
		if len(model.Routes) == 0 || len(model.Routes) > 3 {
			return nil, fmt.Errorf("provider model catalog has an invalid route count")
		}
		config := dto.AdvancedCustomConfig{Routes: model.Routes}
		if err := config.Validate(); err != nil {
			return nil, fmt.Errorf("provider model catalog has invalid relay routes")
		}
		for _, route := range model.Routes {
			_, declared := plugin.Meta.RelayRoute(route.IncomingPath, model.ID)
			target := slices.ContainsFunc(plugin.Meta.Relay, func(template dto.AdvancedCustomRoute) bool {
				return template.IncomingPath != dto.AdvancedCustomModelListPath && template.UpstreamPath == route.UpstreamPath
			})
			if !declared || !target || route.IncomingPath == dto.AdvancedCustomModelListPath || route.Auth != nil || len(route.Models) != 0 || route.PassThroughBodyEnabled {
				return nil, fmt.Errorf("provider model catalog route exceeds its manifest")
			}
		}
	}
	return &catalog, nil
}

func VerifiedManagementHTTPClient(proxy string) (*http.Client, error) {
	if common.TLSInsecureSkipVerify {
		return nil, fmt.Errorf("provider model and pricing discovery requires verified TLS")
	}
	client, err := service.GetHttpClientWithProxySettings(proxy, dto.ChannelSettings{})
	if err != nil {
		return nil, err
	}
	if transport, ok := client.Transport.(*http.Transport); ok && transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		return nil, fmt.Errorf("provider model and pricing discovery requires verified TLS")
	}
	return client, nil
}

// ModelCatalogClient applies management header overrides without forwarding
// client headers. Catalogs always use verified TLS and never follow redirects.
func ModelCatalogClient(info *relaycommon.RelayInfo) (ManagementClient, error) {
	client, err := VerifiedManagementHTTPClient(info.ChannelSetting.Proxy)
	if err != nil {
		return ManagementClient{}, err
	}
	noRedirects := *client
	noRedirects.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	headers := maps.Clone(info.HeadersOverride)
	return ManagementClient{
		Override: func(header http.Header, key string) error {
			managementInfo := &relaycommon.RelayInfo{IsChannelTest: true, ChannelMeta: &relaycommon.ChannelMeta{ApiKey: key, HeadersOverride: headers}}
			overrides, err := channel.ResolveHeaderOverride(managementInfo, nil)
			if err != nil {
				return err
			}
			for name, value := range overrides {
				header.Set(name, value)
			}
			return nil
		},
		Send: noRedirects.Do,
	}, nil
}

func (a *Adaptor) prepareCatalogRoute(c *gin.Context, info *relaycommon.RelayInfo) error {
	pinned, ok := common.GetContextKeyType[jsplugin.PinnedPlugin](c, constant.ContextKeyProviderPlugin)
	if !ok || pinned.Plugin == nil || !pinned.Plugin.Meta.CatalogRouting || a.catalogResolved {
		return nil
	}
	if pinned.Plugin.Meta.Key == "github-copilot" {
		if err := model.CheckCopilotPricing(info.ChannelId, info.GetBillingModelName(), info.UpstreamModelName, common.GetTimestamp()); err != nil {
			return types.NewErrorWithStatusCode(fmt.Errorf("%s", i18n.T(c, err.Error())), types.ErrorCodeModelPriceError, http.StatusServiceUnavailable)
		}
	}
	if err := a.prepareKey(c, info); err != nil {
		return types.NewError(err, types.ErrorCodeDoRequestFailed)
	}
	client, err := ModelCatalogClient(info)
	if err != nil {
		return types.NewError(err, types.ErrorCodeDoRequestFailed)
	}
	catalog, err := FetchModelCatalog(c.Request.Context(), a.plugin, info, client, false)
	if err != nil {
		logger.LogWarn(c, "provider model catalog unavailable: plugin=%s channel_id=%d", a.plugin.Meta.Key, info.ChannelId)
		return types.NewError(fmt.Errorf("provider model catalog unavailable; refresh the channel models before retrying"), types.ErrorCodeDoRequestFailed)
	}
	if a.credential != nil {
		a.useCredentialLine(c, info, info.ApiKey)
	}
	for _, model := range catalog.Data {
		if model.ID != info.UpstreamModelName {
			continue
		}
		config := &dto.AdvancedCustomConfig{Routes: model.Routes}
		route, ok := config.MatchPath(c.Request.URL.Path)
		if !ok {
			break
		}
		a.route = route
		a.catalogWebSocket = model.ResponsesWebSocket
		if a.credential != nil {
			route.Auth = &dto.AdvancedCustomRouteAuth{Type: dto.AdvancedCustomAuthTypeNone}
		}
		info.ChannelOtherSettings.AdvancedCustom = &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{route}}
		common.SetContextKey(c, constant.ContextKeyChannelOtherSetting, info.ChannelOtherSettings)
		a.catalogResolved = true
		return nil
	}
	return types.NewError(fmt.Errorf("provider catalog does not support this model and endpoint"), types.ErrorCodeInvalidRequest, types.ErrOptionWithStatusCode(http.StatusBadRequest), types.ErrOptionWithSkipRetry())
}
