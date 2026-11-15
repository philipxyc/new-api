package plugins

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubCopilotDeviceAuthorizationAndRefresh(t *testing.T) {
	plugin, ok := jsplugin.DefaultRegistry.Generation().Get("github-copilot")
	require.True(t, ok)
	const origin = "https://api.githubcopilot.com"
	now := common.GetTimestamp() + 3600
	polls := 0
	var exchangedTokens []string
	ctx := jsplugin.WithFetcher(t.Context(), func(_ context.Context, request jsplugin.FetchRequest) (jsplugin.FetchResponse, error) {
		var body map[string]any
		if request.Body != "" {
			require.NoError(t, common.UnmarshalJsonStr(request.Body, &body))
			assert.Equal(t, "Iv1.b507a08c87ecfe98", body["client_id"])
		}
		var result any
		switch request.URL {
		case "https://github.com/login/device/code":
			assert.Equal(t, http.MethodPost, request.Method)
			assert.Equal(t, "read:user", body["scope"])
			result = map[string]any{"device_code": "private-device", "user_code": "ABCD-EFGH", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5}
		case "https://github.com/login/oauth/access_token":
			assert.Equal(t, "urn:ietf:params:oauth:grant-type:device_code", body["grant_type"])
			assert.Equal(t, "private-device", body["device_code"])
			polls++
			switch polls {
			case 1:
				result = map[string]any{"error": "authorization_pending"}
			case 2:
				result = map[string]any{"error": "slow_down"}
			default:
				result = map[string]any{"access_token": "private-github", "token_type": "bearer"}
			}
		case "https://api.github.com/user":
			assert.Equal(t, "Bearer private-github", request.Headers["Authorization"])
			result = map[string]any{"id": 123, "login": "test-account"}
		case "https://api.github.com/copilot_internal/v2/token":
			exchangedTokens = append(exchangedTokens, request.Headers["Authorization"])
			result = map[string]any{"token": "private-copilot", "expires_at": now, "endpoints": map[string]string{"api": origin}}
		default:
			t.Fatalf("unexpected credential destination %q", request.URL)
		}
		encoded, err := common.Marshal(result)
		require.NoError(t, err)
		return jsplugin.FetchResponse{Status: http.StatusOK, Body: encoded}, nil
	})
	login := jsplugin.LoginContext{}
	step, err := plugin.CallLogin(ctx, origin, login, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, jsplugin.LoginStepDevice, step.Type)
	assert.Equal(t, "ABCD-EFGH", step.UserCode)
	assert.NotContains(t, step.VerificationURI, "private-device")
	login.Session = step.Session
	for _, interval := range []int{5, 10, 1} {
		step, err = plugin.CallLogin(ctx, origin, login, nil, map[string]any{"poll": true})
		require.NoError(t, err)
		require.Equal(t, jsplugin.LoginStepPending, step.Type)
		assert.Equal(t, interval, step.Interval)
		if len(step.Session) != 0 {
			login.Session = step.Session
		}
	}
	step, err = plugin.CallLogin(ctx, origin, login, nil, map[string]any{"poll": true})
	require.NoError(t, err)
	require.Equal(t, jsplugin.LoginStepDone, step.Type)
	require.NotNil(t, step.Result)
	assert.Equal(t, "123", step.Result.AccountID)
	assert.Equal(t, "test-account", step.Result.Label)
	assert.Equal(t, now, step.Result.ExpiresAt)
	assert.Empty(t, step.Result.Headers, "credentials are attached only after checking the final request URL")

	signed, err := plugin.CallSignRequest(ctx, jsplugin.CredentialContext{BaseURL: origin, Credential: step.Result.Credential},
		http.MethodPost, origin+"/chat/completions", nil, "")
	require.NoError(t, err)
	assert.Equal(t, "Bearer private-copilot", signed.Headers["Authorization"])
	assert.Equal(t, "user", signed.Headers["X-Initiator"], "do not disguise user traffic as free agent traffic")

	refreshed, err := plugin.CallRefresh(ctx, jsplugin.CredentialContext{BaseURL: origin, Credential: step.Result.Credential})
	require.NoError(t, err)
	assert.False(t, refreshed.Relogin)
	assert.Equal(t, now, refreshed.ExpiresAt)
	assert.Empty(t, refreshed.AccountID, "the host preserves the original account identity")
	assert.Equal(t, []string{"Bearer private-github", "Bearer private-github"}, exchangedTokens)
}

func TestGitHubCopilotRejectsCredentialExfiltrationAndExpiredFlows(t *testing.T) {
	plugin, ok := jsplugin.DefaultRegistry.Generation().Get("github-copilot")
	require.True(t, ok)
	const origin = "https://api.githubcopilot.com"
	credential := []byte(`{"githubToken":"private-github","copilotToken":"private-copilot","apiOrigin":"https://api.githubcopilot.com"}`)
	for _, target := range []string{
		"http://api.githubcopilot.com/chat/completions",
		"https://api.githubcopilot.com.evil.example/chat/completions",
		"https://api.githubcopilot.com@evil.example/chat/completions",
		"https://api.business.githubcopilot.com/chat/completions",
	} {
		t.Run(target, func(t *testing.T) {
			_, err := plugin.CallSignRequest(t.Context(), jsplugin.CredentialContext{BaseURL: origin, Credential: credential},
				http.MethodPost, target, nil, "")
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-")
		})
	}
	for _, base := range []string{"http://api.githubcopilot.com", "https://evil.example", origin + "/path"} {
		_, err := plugin.CallLogin(t.Context(), base, jsplugin.LoginContext{}, nil, nil)
		require.ErrorContains(t, err, "official GitHub Copilot API origin")
	}
	_, err := plugin.CallLogin(t.Context(), origin, jsplugin.LoginContext{Session: []byte(`{"deviceCode":"private-device","expiresAt":1}`)},
		nil, map[string]any{"poll": true})
	require.ErrorContains(t, err, "expired")
	assert.NotContains(t, err.Error(), "private-device")
}

func TestGitHubCopilotRejectsInvalidAuthorizations(t *testing.T) {
	plugin, ok := jsplugin.DefaultRegistry.Generation().Get("github-copilot")
	require.True(t, ok)
	const origin = "https://api.githubcopilot.com"
	cases := []struct {
		name string
		url  string
		body string
	}{
		{"untrusted verification page", "/login/device/code", `{"device_code":"private-code","user_code":"ABCD","verification_uri":"https://evil.example","expires_in":900,"interval":5}`},
		{"denied device code", "/login/oauth/access_token", `{"error":"access_denied","error_description":"private-secret"}`},
		{"invalid bearer token", "/login/oauth/access_token", `{"access_token":"private-token\ninjected","token_type":"bearer"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := jsplugin.WithFetcher(t.Context(), func(_ context.Context, request jsplugin.FetchRequest) (jsplugin.FetchResponse, error) {
				assert.True(t, strings.HasSuffix(request.URL, tc.url))
				return jsplugin.FetchResponse{Status: http.StatusOK, Body: []byte(tc.body)}, nil
			})
			var input any
			login := jsplugin.LoginContext{}
			if tc.url != "/login/device/code" {
				input = map[string]any{"poll": true}
				login.Session = []byte(`{"deviceCode":"private-code","interval":5,"expiresAt":4102444800}`)
			}
			_, err := plugin.CallLogin(ctx, origin, login, nil, input)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-")
		})
	}
}

func TestGitHubCopilotRevocationAndTokenEndpointValidation(t *testing.T) {
	plugin, ok := jsplugin.DefaultRegistry.Generation().Get("github-copilot")
	require.True(t, ok)
	const origin = "https://api.githubcopilot.com"
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		relogin bool
	}{
		{"revoked authorization", 401, `{"secret":"private-token"}`, true},
		{"missing entitlement", 403, `{}`, true},
		{"transient outage", 503, `{"secret":"private-token"}`, false},
		{"wrong subscription origin", 200, `{"token":"private-token","expires_at":4102444800,"endpoints":{"api":"https://api.business.githubcopilot.com"}}`, false},
		{"untrusted origin", 200, `{"token":"private-token","expires_at":4102444800,"endpoints":{"api":"https://evil.example"}}`, false},
		{"expired token", 200, `{"token":"private-token","expires_at":1,"endpoints":{"api":"https://api.githubcopilot.com"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := jsplugin.WithFetcher(t.Context(), func(_ context.Context, request jsplugin.FetchRequest) (jsplugin.FetchResponse, error) {
				assert.Equal(t, "https://api.github.com/copilot_internal/v2/token", request.URL)
				return jsplugin.FetchResponse{Status: tc.status, Body: []byte(tc.body)}, nil
			})
			result, err := plugin.CallRefresh(ctx, jsplugin.CredentialContext{BaseURL: origin, Credential: []byte(`{"githubToken":"private-github"}`)})
			if tc.relogin {
				require.NoError(t, err)
				assert.True(t, result.Relogin)
			} else {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "private-")
				if tc.name == "wrong subscription origin" {
					assert.Contains(t, err.Error(), "https://api.business.githubcopilot.com")
					assert.Contains(t, err.Error(), "without restarting")
				}
			}
		})
	}
}

func TestGitHubCopilotModelDiscovery(t *testing.T) {
	plugin, ok := jsplugin.DefaultRegistry.Generation().Get("github-copilot")
	require.True(t, ok)
	result, upstreamErr, err := plugin.CallConvertResponse(t.Context(), jsplugin.HookContext{Path: "/v1/models"}, http.StatusOK, nil,
		[]byte(`{"data":[
			{"id":"chat","supported_endpoints":["/chat/completions"]},
			{"id":"gpt-6.1-terra","capabilities":{"type":"chat"},"supported_endpoints":["/responses"]},
			{"id":"unverified-responses-model","supported_endpoints":["/responses"]},
			{"id":"legacy","capabilities":{"type":"chat"}},
			{"id":"text-embedding-3-small","capabilities":{"type":"embeddings"}},
			{"id":"decision","capabilities":{"type":"decisions"},"supported_endpoints":["/chat/completions"]},
			{"id":"disabled","supported_endpoints":[]},{"id":""}
		]}`))
	require.NoError(t, err)
	require.Nil(t, upstreamErr)
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(result.Body, &catalog))
	var ids []string
	for _, entry := range catalog.Data {
		ids = append(ids, entry.ID)
	}
	assert.Equal(t, []string{"chat", "gpt-6.1-terra", "legacy"}, ids)
}

func TestGitHubCopilotResponsesModelRouting(t *testing.T) {
	plugin, ok := jsplugin.DefaultRegistry.Generation().Get("github-copilot")
	require.True(t, ok)
	for _, tc := range []struct {
		path, converter, fallback string
	}{
		{"/v1/chat/completions", "openai_chat_completions_to_openai_responses", ""},
		{"/v1/responses", "", "openai_responses_to_openai_chat_completions"},
		{"/v1/messages", "claude_messages_to_openai_responses", "anthropic_messages_to_openai_chat_completions"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			for _, name := range []string{"gpt-5.3-codex", "gpt-5.4-nano", "gpt-5.6-luna", "gpt-5.6-sol-fast", "gpt-5.6-sol",
				"gpt-5.6-terra", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-astra", "gpt-6-luna", "gpt-6-sol", "grok-4.6", "grok-4.7", "mai-code-1.1-flash"} {
				for _, prefix := range []string{"", "github-copilot/"} {
					route, found := plugin.Meta.RelayRoute(tc.path, prefix+name)
					require.True(t, found)
					assert.Equal(t, "/responses", route.UpstreamPath, prefix+name)
					assert.EqualValues(t, tc.converter, route.Converter, prefix+name)
				}
			}
			for _, name := range []string{"gpt-4.1", "claude-sonnet-5", "github-copilot/claude-haiku-4.5"} {
				route, found := plugin.Meta.RelayRoute(tc.path, name)
				require.True(t, found)
				assert.Equal(t, "/chat/completions", route.UpstreamPath, name)
				assert.EqualValues(t, tc.fallback, route.Converter, name)
			}
		})
	}
}

func TestGitHubCopilotExpiringAuthorization(t *testing.T) {
	plugin, ok := jsplugin.DefaultRegistry.Generation().Get("github-copilot")
	require.True(t, ok)
	const origin = "https://api.githubcopilot.com"
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			now := common.GetTimestamp()
			ctx := jsplugin.WithFetcher(t.Context(), func(_ context.Context, request jsplugin.FetchRequest) (jsplugin.FetchResponse, error) {
				if request.URL == "https://github.com/login/oauth/access_token" {
					var body map[string]any
					require.NoError(t, common.UnmarshalJsonStr(request.Body, &body))
					assert.Equal(t, "refresh_token", body["grant_type"])
					assert.Equal(t, "private-refresh", body["refresh_token"])
					if status != http.StatusOK {
						return jsplugin.FetchResponse{Status: status, Body: []byte(`{"error":"invalid_grant"}`)}, nil
					}
					return jsplugin.FetchResponse{Status: status, Body: []byte(`{"access_token":"rotated-github","refresh_token":"rotated-refresh","token_type":"bearer","expires_in":3600}`)}, nil
				}
				require.Equal(t, "https://api.github.com/copilot_internal/v2/token", request.URL)
				assert.Equal(t, "Bearer rotated-github", request.Headers["Authorization"])
				body, err := common.Marshal(map[string]any{"token": "rotated-copilot", "expires_at": now + 7200, "endpoints": map[string]string{"api": origin}})
				require.NoError(t, err)
				return jsplugin.FetchResponse{Status: http.StatusOK, Body: body}, nil
			})
			result, err := plugin.CallRefresh(ctx, jsplugin.CredentialContext{
				BaseURL:    origin,
				Credential: []byte(`{"githubToken":"private-old","githubRefreshToken":"private-refresh","githubExpiresAt":1}`),
			})
			require.NoError(t, err)
			if status != http.StatusOK {
				assert.True(t, result.Relogin)
				assert.Empty(t, result.Credential)
				return
			}
			assert.False(t, result.Relogin)
			assert.InDelta(t, now+3600, result.ExpiresAt, 2)
			var credential map[string]any
			require.NoError(t, common.Unmarshal(result.Credential, &credential))
			assert.Equal(t, "rotated-github", credential["githubToken"])
			assert.Equal(t, "rotated-refresh", credential["githubRefreshToken"])
		})
	}
}

func TestGitHubCopilotVisionHeaders(t *testing.T) {
	plugin, ok := jsplugin.DefaultRegistry.Generation().Get("github-copilot")
	require.True(t, ok)
	for _, body := range []string{
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/image.png"}]}]}`,
	} {
		prepared, err := plugin.CallPrepareRequest(t.Context(), jsplugin.HookContext{}, jsplugin.PrepareInput{Body: []byte(body)})
		require.NoError(t, err)
		assert.JSONEq(t, `[{"type":"header.patch","set":{"Copilot-Vision-Request":"true"}}]`, string(prepared.Effects))
	}
}
