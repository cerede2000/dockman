package secrets

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	hostmiddleware "github.com/RA341/dockman/internal/host/middleware"
)

type HTTPHandler struct{ service *Service }

type writeInput struct {
	StackPath string `json:"stackPath"`
	Value     string `json:"value"`
	Encoding  string `json:"encoding,omitempty"`
}

type readOutput struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Encoding string `json:"encoding"`
}

func NewHTTPHandler(service *Service) http.Handler {
	h := &HTTPHandler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", h.status)
	mux.HandleFunc("GET /", h.list)
	mux.HandleFunc("GET /{name}", h.read)
	mux.HandleFunc("PUT /{name}", h.write)
	mux.HandleFunc("DELETE /{name}", h.delete)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func (h *HTTPHandler) status(w http.ResponseWriter, r *http.Request) {
	host, ok := requestHost(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "host": host, "runtimeDirectory": RuntimeDirectory,
		"modes": []string{"plain_file"}, "maxSecretBytes": MaxSecretBytes,
	})
}

func (h *HTTPHandler) list(w http.ResponseWriter, r *http.Request) {
	host, ok := requestHost(w, r)
	if !ok {
		return
	}
	items, err := h.service.List(host, r.URL.Query().Get("stack"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *HTTPHandler) read(w http.ResponseWriter, r *http.Request) {
	host, ok := requestHost(w, r)
	if !ok {
		return
	}
	value, err := h.service.Read(host, r.URL.Query().Get("stack"), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	defer clear(value)
	writeJSON(w, http.StatusOK, readOutput{Name: r.PathValue("name"), Value: base64.StdEncoding.EncodeToString(value), Encoding: "base64"})
}

func (h *HTTPHandler) write(w http.ResponseWriter, r *http.Request) {
	host, ok := requestHost(w, r)
	if !ok {
		return
	}
	var input writeInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxSecretBytes*2))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "invalid secret request", http.StatusBadRequest)
		return
	}
	var value []byte
	var err error
	switch strings.ToLower(strings.TrimSpace(input.Encoding)) {
	case "", "utf-8", "utf8":
		value = []byte(input.Value)
	case "base64":
		value, err = base64.StdEncoding.DecodeString(input.Value)
	default:
		http.Error(w, "encoding must be utf-8 or base64", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "invalid base64 secret value", http.StatusBadRequest)
		return
	}
	defer clear(value)
	item, err := h.service.Write(host, input.StackPath, r.PathValue("name"), value)
	input.Value = ""
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *HTTPHandler) delete(w http.ResponseWriter, r *http.Request) {
	host, ok := requestHost(w, r)
	if !ok {
		return
	}
	if err := h.service.Delete(host, r.URL.Query().Get("stack"), r.PathValue("name")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func requestHost(w http.ResponseWriter, r *http.Request) (string, bool) {
	host, err := hostmiddleware.GetHost(r.Context())
	if err != nil {
		http.Error(w, "host not provided", http.StatusBadRequest)
		return "", false
	}
	return host, true
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrInvalidName), errors.Is(err, ErrInvalidStackPath):
		status = http.StatusBadRequest
	case errors.Is(err, ErrSecretTooLarge):
		status = http.StatusRequestEntityTooLarge
	case errors.Is(err, fs.ErrNotExist):
		status = http.StatusNotFound
	case errors.Is(err, fs.ErrPermission):
		status = http.StatusForbidden
	}
	// Errors contain paths and operation names, never secret contents.
	http.Error(w, err.Error(), status)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
