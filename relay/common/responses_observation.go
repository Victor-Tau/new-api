package common

import (
	"cmp"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

const codexSubagentHeader = "x-openai-subagent"

// responsesSubagentKind observes the Codex client's metadata for one request.
// A frame's identity takes precedence over the reused connection's headers.
func responsesSubagentKind(c *gin.Context, clientMetadataBody, frameBody []byte) string {
	clientMetadata := ojson.GetBytesNoCopy(clientMetadataBody, "@this")
	headerMetadata := strings.TrimSpace(c.GetHeader(codexTurnMetadataHeader))
	headerKind := codexKind(c.GetHeader(codexSubagentHeader))
	clientKind := codexKind(clientMetadata.Get(codexSubagentHeader).Str)
	frameMetadata := clientMetadata.Get(codexTurnMetadataHeader)
	if frameBody != nil && frameMetadata.Exists() {
		return cmp.Or(codexTurnSubagentKind(frameMetadata), clientKind)
	}
	var bodyMetadata []ojson.Result
	paths := []string{"turnMetadata", "turn_metadata", codexTurnMetadataHeader, "client_metadata"}
	if frameBody != nil {
		bodyMetadata = ojson.GetManyBytes(frameBody, paths...)
	} else if headerMetadata == "" || !clientMetadata.Exists() {
		// The HTTP validator already stored the body. Read only the alias fields.
		if value, ok := c.Get(common.KeyBodyStorage); ok {
			if storage, ok := value.(common.BodyStorage); ok {
				bodyMetadata, _ = common.ReadBodyJSONFields(storage, paths...)
			}
		}
	}
	if len(bodyMetadata) == len(paths) && !clientMetadata.Exists() {
		clientMetadata = bodyMetadata[3]
		clientKind = codexKind(clientMetadata.Get(codexSubagentHeader).Str)
		frameMetadata = clientMetadata.Get(codexTurnMetadataHeader)
	}
	var turnMetadata ojson.Result
	if len(bodyMetadata) == len(paths) {
		turnMetadata = firstCodexTurnMetadata(bodyMetadata[:3])
	}
	if !turnMetadata.Exists() {
		turnMetadata = firstCodexTurnMetadata([]ojson.Result{
			frameMetadata, clientMetadata.Get("turnMetadata"), clientMetadata.Get("turn_metadata"),
		})
	}
	if frameBody != nil && (turnMetadata.Exists() || clientKind != "") {
		return cmp.Or(codexTurnSubagentKind(turnMetadata), clientKind)
	}
	if headerMetadata != "" {
		turnMetadata = ojson.Result{Type: ojson.String, Str: headerMetadata}
	}
	return cmp.Or(codexTurnSubagentKind(turnMetadata), headerKind, clientKind)
}

func decodeCodexTurnMetadata(metadata ojson.Result) ojson.Result {
	if metadata.Type == ojson.String {
		if raw := metadata.String(); ojson.Valid(raw) {
			return ojson.Parse(raw)
		}
	}
	return metadata
}

func codexTurnSubagentKind(metadata ojson.Result) string {
	return codexKind(decodeCodexTurnMetadata(metadata).Get("subagent_kind").Str)
}

func firstCodexTurnMetadata(fields []ojson.Result) ojson.Result {
	for _, field := range fields {
		if field.Exists() {
			return field
		}
	}
	return ojson.Result{}
}

func codexKind(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 64 || strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) >= 0 {
		return ""
	}
	return value
}
