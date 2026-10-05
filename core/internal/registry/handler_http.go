package registry

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"gorm.io/gorm"
)

type HTTPHandler struct{ service *Service }

// NewHTTPHandler exposes the credentials under /api/protected/registries,
// the same shape the Git credentials use.
func NewHTTPHandler(service *Service) http.Handler {
	h := &HTTPHandler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /credentials", h.list)
	mux.HandleFunc("POST /credentials", h.create)
	mux.HandleFunc("PUT /credentials/{id}", h.update)
	mux.HandleFunc("DELETE /credentials/{id}", h.delete)
	mux.HandleFunc("POST /credentials/test", h.test)
	mux.HandleFunc("POST /credentials/{id}/test", h.testSaved)
	return mux
}

func (h *HTTPHandler) list(w http.ResponseWriter, _ *http.Request) {
	rows, err := h.service.List()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *HTTPHandler) create(w http.ResponseWriter, r *http.Request) {
	var input Input
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.Create(input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (h *HTTPHandler) update(w http.ResponseWriter, r *http.Request) {
	var input Input
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.Update(r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.service.Delete(r.PathValue("id")); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *HTTPHandler) test(w http.ResponseWriter, r *http.Request) {
	h.runTest(w, r, "")
}

func (h *HTTPHandler) testSaved(w http.ResponseWriter, r *http.Request) {
	h.runTest(w, r, r.PathValue("id"))
}

func (h *HTTPHandler) runTest(w http.ResponseWriter, r *http.Request, credentialUUID string) {
	var input Input
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &input); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := h.service.Test(r.Context(), credentialUUID, input); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeAPIError(w, http.StatusNotFound, "Registry credential not found")
			return
		}
		// A refused login is an answer, not a server fault: the UI shows it
		// next to the credential.
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
		writeAPIError(w, http.StatusNotFound, "Registry credential not found")
	case strings.Contains(strings.ToLower(err.Error()), "unique constraint"),
		strings.Contains(err.Error(), "already exists"):
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
