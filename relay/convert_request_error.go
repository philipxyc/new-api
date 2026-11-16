package relay

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitreasoning "github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func newConvertRequestFailedError(c *gin.Context, info *relaycommon.RelayInfo, err error) *types.NewAPIError {
	// A request plugin's rejection or failure keeps its own status and code.
	var pluginErr *types.NewAPIError
	if errors.As(err, &pluginErr) && (pluginErr.GetErrorCode() == types.ErrorCodeRequestRejected || pluginErr.GetErrorCode() == types.ErrorCodeRequestPluginFailed) {
		return pluginErr
	}
	// Catalog routing prepares the provider before conversion. Credential,
	// transport and source-verification failures keep ordinary channel retries.
	if pluginErr != nil && info != nil && info.ApiType == constant.APITypeProviderPlugin {
		switch pluginErr.GetErrorCode() {
		case types.ErrorCodeDoRequestFailed, types.ErrorCodeChannelInvalidKey, types.ErrorCodeBadResponseStatusCode, types.ErrorCodeModelPriceError:
			return pluginErr
		}
	}
	var loss *types.ConversionLossError
	if errors.As(err, &loss) {
		info.RecordConversionDiagnostics(c, loss.Diagnostics)
		return types.NewErrorWithStatusCode(err, types.ErrorCodeConvertRequestFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if kitreasoning.IsClientError(err) {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeConvertRequestFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
}
