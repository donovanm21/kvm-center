package main

import (
	"fmt"
	"net/http"
)

// Upstream credentials belong to the server-to-agent leg. An agent rejection
// must not impersonate a browser session failure or a user permission denial.
// Keep both boundaries intact: the caller still receives an unsuccessful 502.
func agentReadAuthenticationError(w http.ResponseWriter, status int) bool {
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return false
	}
	writeError(w, http.StatusBadGateway, fmt.Sprintf("agent rejected the server request (upstream %d); check the server-to-agent connection", status))
	return true
}
