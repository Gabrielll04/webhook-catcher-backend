package httpapi

import (
	"encoding/json"
	"net/http"
)

type errorBody struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Details   any    `json:"details"`
	RequestID string `json:"request_id"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, requestID, code, message string, details any) {
	writeJSON(w, status, errorBody{Error: errorPayload{
		Code:      code,
		Message:   message,
		Details:   details,
		RequestID: requestID,
	}})
}
