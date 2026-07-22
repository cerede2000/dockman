package gitsync

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"gorm.io/gorm"
)

type HTTPHandler struct{ service *Service }

func NewHTTPHandler(service *Service) http.Handler {
	h := &HTTPHandler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", h.status)
	mux.HandleFunc("GET /credentials", h.listCredentials)
	mux.HandleFunc("POST /credentials", h.createCredential)
	mux.HandleFunc("PUT /credentials/{id}", h.updateCredential)
	mux.HandleFunc("DELETE /credentials/{id}", h.deleteCredential)
	mux.HandleFunc("POST /credentials/test", h.testCredential)
	mux.HandleFunc("POST /credentials/{id}/test", h.testSavedCredential)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func (h *HTTPHandler) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":                 h.service.Enabled(),
		"phase":                   "credentials",
		"repositorySyncAvailable": false,
	})
}

func (h *HTTPHandler) requireEnabled(w http.ResponseWriter) bool {
	if h.service.Enabled() {
		return true
	}
	writeAPIError(w, http.StatusNotFound, "Git synchronization is disabled")
	return false
}

func (h *HTTPHandler) listCredentials(w http.ResponseWriter, _ *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	rows, err := h.service.ListCredentials()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *HTTPHandler) createCredential(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input CredentialInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.CreateCredential(input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (h *HTTPHandler) updateCredential(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input CredentialInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.UpdateCredential(r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	if err := h.service.DeleteCredential(r.PathValue("id")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) testCredential(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input TestInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.service.TestCredential(r.Context(), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) testSavedCredential(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input struct {
		RepositoryURL string `json:"repositoryUrl"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &input); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	result, err := h.service.TestSavedCredential(r.Context(), r.PathValue("id"), input.RepositoryURL)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		writeAPIError(w, http.StatusNotFound, "Git credential not found")
	case strings.Contains(strings.ToLower(err.Error()), "unique constraint"):
		writeAPIError(w, http.StatusConflict, "A Git credential with this name already exists")
	case strings.Contains(err.Error(), "still used"):
		writeAPIError(w, http.StatusConflict, err.Error())
	default:
		writeAPIError(w, http.StatusBadRequest, err.Error())
	}
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
