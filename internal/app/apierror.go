package app

import (
	"encoding/json"
	"strings"
)

func apiErrorMessage(payload []byte) string {
	var wrapped struct {
		Error struct {
			Code    int    `json:"code"`
			Status  string `json:"status"`
			Message string `json:"message"`
			Details []struct {
				Type   string `json:"@type"`
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &wrapped) == nil && wrapped.Error.Message != "" {
		for _, d := range wrapped.Error.Details {
			if strings.HasSuffix(d.Type, "google.rpc.ErrorInfo") && d.Reason != "" {
				return d.Reason + ": " + wrapped.Error.Message
			}
		}
		return wrapped.Error.Message
	}

	var flat struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &flat) == nil && flat.Message != "" {
		if flat.Reason != "" {
			return flat.Reason + ": " + flat.Message
		}
		return flat.Message
	}

	var errStr struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(payload, &errStr) == nil && errStr.Error != "" {
		return errStr.Error
	}

	if message := strings.TrimSpace(string(payload)); message != "" {
		return message
	}
	return "request failed"
}
