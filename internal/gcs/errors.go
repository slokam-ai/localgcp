package gcs

import (
	"encoding/json"
	"net/http"
)

// gcpError matches the Google Cloud Storage JSON API error envelope.
type gcpError struct {
	Error gcpErrorBody `json:"error"`
}

type gcpErrorBody struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Errors  []gcpErrorDetail `json:"errors"`
}

type gcpErrorDetail struct {
	Message string `json:"message"`
	Domain  string `json:"domain"`
	Reason  string `json:"reason"`
}

func writeError(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(gcpError{
		Error: gcpErrorBody{
			Code:    code,
			Message: message,
			Errors: []gcpErrorDetail{
				{Message: message, Domain: "global", Reason: reason},
			},
		},
	})
}

func writeNotFound(w http.ResponseWriter, message string) {
	writeError(w, http.StatusNotFound, "notFound", message)
}

func writeConflict(w http.ResponseWriter, message string) {
	writeError(w, http.StatusConflict, "conflict", message)
}

func writeBadRequest(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, "invalid", message)
}

func writeNotImplemented(w http.ResponseWriter, message string) {
	writeError(w, http.StatusNotImplemented, "notImplemented", "localgcp: "+message)
}
