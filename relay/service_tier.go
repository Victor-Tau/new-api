package relay

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// observeUpstreamServiceTier records the tier in the prepared upstream body.
// Passthrough bodies can be on disk, so read only the field needed for the log.
func observeUpstreamServiceTier(info *relaycommon.RelayInfo, body []byte, storage common.BodyStorage) {
	var field ojson.Result
	if storage != nil {
		fields, err := common.ReadBodyJSONFields(storage, "service_tier")
		if err == nil && len(fields) > 0 {
			field = fields[0]
		}
	} else {
		field = ojson.GetBytesNoCopy(body, "service_tier")
	}
	info.UpstreamServiceTier = ""
	if field.Type == ojson.String {
		if tier := strings.TrimSpace(field.Str); len(tier) <= 64 {
			info.UpstreamServiceTier = strings.Clone(tier)
		}
	}
}
