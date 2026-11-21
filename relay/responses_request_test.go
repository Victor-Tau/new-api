package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relay/requestplugin"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/request_plugin_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesCompactionLog(t *testing.T) {
	const compaction = `{"request_kind":"compaction"}`
	for _, tc := range []struct {
		name, header, fields          string
		socket, explicit, passthrough bool
		compact                       bool
	}{
		{name: "header survives passthrough", header: compaction, fields: `"input":"hello"`, passthrough: true, compact: true},
		{name: "Codex metadata in WebSocket frame", fields: `"input":"hello","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"compaction\"}"}`, socket: true, compact: true},
		{name: "flat client metadata", fields: `"input":"hello","client_metadata":{"request_kind":"compaction"}`, compact: true},
		{name: "body metadata", fields: `"input":"hello","metadata":{"request_kind":"compaction"}`, compact: true},
		{name: "trigger with ordinary request kind", header: `{"request_kind":"turn"}`, fields: `"input":[{"type":"message"},{"type":"compaction_trigger"}]`, compact: true},
		{name: "per-frame metadata overrides handshake", header: compaction, fields: `"input":[{"type":"compaction"}],"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\"}"}`, socket: true},
		{name: "malformed header and text input", header: `{"request_kind":"compaction"`, fields: `"input":"compaction_trigger"`},
		{name: "trigger outside input array", fields: `"input":{"type":"compaction_trigger"}`},
		{name: "explicit compact endpoint", fields: `"input":"hello"`, explicit: true, compact: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, method := "/v1/responses", http.MethodPost
			format := types.RelayFormat(types.RelayFormatOpenAIResponses)
			mode := relayconstant.RelayModeResponses
			if tc.explicit {
				path += "/compact"
				format = types.RelayFormatOpenAIResponsesCompaction
				mode = relayconstant.RelayModeResponsesCompact
			}
			if tc.socket {
				method = http.MethodGet
			}
			raw := `{"model":"gpt-5.1-codex",` + tc.fields + `}`
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(method, path, strings.NewReader(raw))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("x-codex-turn-metadata", tc.header)
			t.Cleanup(func() { common.CleanupBodyStorage(c) })

			var request dto.Request
			if tc.socket {
				create, _, err := normalizeResponsesWSTestMessage([]byte(`{"type":"response.create","response":` + raw + `}`))
				require.NoError(t, err)
				request = &create.Request
			} else {
				var err error
				request, err = helper.GetAndValidateRequest(c, format)
				require.NoError(t, err)
			}
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "gpt-5.1-codex")
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://api.openai.com")
			info, err := relaycommon.GenRelayInfo(c, format, request, nil)
			require.NoError(t, err)
			assert.Equal(t, mode, info.RelayMode)

			if tc.passthrough {
				common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: true})
				adaptor, body, closer, apiErr := PrepareResponsesRequest(c, info, request.(*dto.OpenAIResponsesRequest))
				require.Nil(t, apiErr)
				t.Cleanup(func() { require.NoError(t, closer.Close()) })
				wire, err := io.ReadAll(body)
				require.NoError(t, err)
				assert.Equal(t, raw, string(wire))
				upstream, err := adaptor.GetRequestURL(info)
				require.NoError(t, err)
				assert.Equal(t, "https://api.openai.com/v1/responses", upstream)
			} else {
				info.InitChannelMeta(c)
			}

			other := service.GenerateTextOtherInfo(c, info, 0, 0, 0, 0, 0, 0, 0).Snapshot()
			if tc.compact {
				assert.Equal(t, true, other["compact"])
			} else {
				assert.NotContains(t, other, "compact")
			}
		})
	}
}

func TestPrepareResponsesRequestDeepSeekEffectiveReasoning(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	previous := settings.PassThroughRequestEnabled
	t.Cleanup(func() { settings.PassThroughRequestEnabled = previous })
	settings.PassThroughRequestEnabled = false
	const input = `[{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"}]`
	for _, tc := range []struct {
		name, effort, override, endpoint         string
		passthrough, replaceInput, wantReasoning bool
		globalPassthrough                        bool
	}{
		{name: "override enables thinking", effort: "none", override: "high", wantReasoning: true},
		{name: "override disables thinking", effort: "high", override: "none"},
		{name: "override replaces history", effort: "high", override: "high", replaceInput: true, wantReasoning: true},
		{name: "no override", effort: "high", wantReasoning: true},
		{name: "thinking disabled", effort: "none"},
		{name: "third party endpoint", effort: "none", override: "high", endpoint: "https://other.example"},
		{name: "passthrough bypasses overrides and normalization", effort: "none", override: "high", passthrough: true},
		{name: "global passthrough bypasses normalization", effort: "high", globalPassthrough: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings.PassThroughRequestEnabled = tc.globalPassthrough
			endpoint := tc.endpoint
			if endpoint == "" {
				endpoint = "https://API.DEEPSEEK.COM:443/v1"
			}
			raw := `{"model":"deepseek-flash","reasoning":{"effort":"` + tc.effort + `"},"input":` + input + `}`
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(raw))
			c.Request.Header.Set("Content-Type", "application/json")
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "deepseek-flash")
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDeepSeek)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, endpoint)
			common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: tc.passthrough})
			if tc.override != "" {
				ops := []any{map[string]any{"path": "reasoning.effort", "mode": "set", "value": tc.override}, map[string]any{"path": "vendor_extension", "mode": "set", "value": "preserved"}}
				if tc.replaceInput {
					var replacement any
					require.NoError(t, common.Unmarshal([]byte(input), &replacement))
					ops = append(ops, map[string]any{"path": "input", "mode": "set", "value": replacement})
				}
				common.SetContextKey(c, constant.ContextKeyChannelParamOverride, map[string]any{"operations": ops})
			}
			var req dto.OpenAIResponsesRequest
			require.NoError(t, common.Unmarshal([]byte(raw), &req))
			info := relaycommon.GenRelayInfoResponses(c, &req)
			_, body, closer, apiErr := PrepareResponsesRequest(c, info, &req)
			require.Nil(t, apiErr)
			t.Cleanup(func() { require.NoError(t, closer.Close()) })
			wire, err := io.ReadAll(body)
			require.NoError(t, err)
			if tc.passthrough || tc.globalPassthrough {
				assert.Equal(t, raw, string(wire))
				other := service.GenerateTextOtherInfo(c, info, 0, 0, 0, 0, 0, 0, 0)
				assert.Equal(t, tc.effort, other.Snapshot()["reasoning_effort"])
				return
			}
			wantEffort := tc.effort
			if tc.override != "" {
				wantEffort = tc.override
				assert.Equal(t, "preserved", gjson.GetBytes(wire, "vendor_extension").String())
			}
			assert.Equal(t, wantEffort, gjson.GetBytes(wire, "reasoning.effort").String())
			if tc.wantReasoning {
				assert.Equal(t, "reasoning", gjson.GetBytes(wire, "input.0.type").String())
				assert.Equal(t, " ", gjson.GetBytes(wire, "input.0.content.0.text").String())
				assert.Equal(t, "function_call", gjson.GetBytes(wire, "input.1.type").String())
			} else {
				assert.JSONEq(t, input, gjson.GetBytes(wire, "input").Raw)
			}
		})
	}
}

// finalizedEgressPlugin rejects a body whose first input item is reasoning,
// which only the DeepSeek finalizer adds to the test request.
const finalizedEgressPlugin = `export const meta = {apiVersion: 1, key: "rp-finalized-egress", name: "Finalized egress", version: "1.0.0", author: {name: "Test"}};
export function egress(ctx, req) {
  const input = req.body && req.body.input;
  if (Array.isArray(input) && input.length && input[0].type === "reasoning") {
    return {action: "reject", code: "finalized.seen", message: "Egress saw the finalized body"};
  }
  return {action: "continue"};
}`

func TestPrepareResponsesRequestEgressReadsTheFinalizedBody(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	previous := settings.PassThroughRequestEnabled
	t.Cleanup(func() { settings.PassThroughRequestEnabled = previous })
	settings.PassThroughRequestEnabled = false
	require.NoError(t, i18n.Init())
	plugin, err := jsplugin.DefaultRegistry.Register(finalizedEgressPlugin, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = jsplugin.DefaultRegistry.Unregister(plugin.Meta.Key) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{request_plugin_setting.InstancesOption: `[{"id":"fe","name":"Finalized","plugin":"rp-finalized-egress","enabled":true,"global":true}]`}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{request_plugin_setting.InstancesOption: "[]"}))
	})

	raw := `{"model":"deepseek-flash","reasoning":{"effort":"high"},"input":[{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"}]}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	t.Cleanup(func() { common.CleanupBodyStorage(c) })
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "deepseek-flash")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDeepSeek)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://api.deepseek.com")
	common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{})
	_, _, apiErr := requestplugin.Ingress(c, types.RelayFormatOpenAIResponses)
	require.Nil(t, apiErr)
	var req dto.OpenAIResponsesRequest
	require.NoError(t, common.Unmarshal([]byte(raw), &req))
	info := relaycommon.GenRelayInfoResponses(c, &req)
	_, _, closer, apiErr := PrepareResponsesRequest(c, info, &req)
	if closer != nil {
		t.Cleanup(func() { _ = closer.Close() })
	}
	require.NotNil(t, apiErr, "egress must read the reasoning item the finalizer added")
	assert.Equal(t, types.ErrorCodeRequestRejected, apiErr.GetErrorCode())
}
