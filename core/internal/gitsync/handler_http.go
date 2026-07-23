package gitsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
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
	mux.HandleFunc("GET /repositories", h.listRepositories)
	mux.HandleFunc("POST /repositories", h.createRepository)
	mux.HandleFunc("POST /repositories/github", h.createGitHubRepository)
	mux.HandleFunc("PUT /repositories/{id}/policy", h.updateRepositoryPolicy)
	mux.HandleFunc("GET /repositories/{id}/status", h.repositoryStatus)
	mux.HandleFunc("POST /repositories/{id}/fetch", h.fetchRepository)
	mux.HandleFunc("POST /repositories/{id}/pull", h.pullRepository)
	mux.HandleFunc("POST /repositories/{id}/push", h.pushRepository)
	mux.HandleFunc("GET /repositories/{id}/operations", h.repositoryOperations)
	mux.HandleFunc("DELETE /repositories/{id}", h.deleteRepository)
	mux.HandleFunc("GET /stack-targets", h.listStackTargets)
	mux.HandleFunc("GET /bindings", h.listBindings)
	mux.HandleFunc("GET /stack-statuses", h.listGitStackStatuses)
	mux.HandleFunc("PUT /bindings/{id}/stack-status/{composePath...}", h.pauseGitStackAutomation)
	mux.HandleFunc("POST /bindings/{id}/stack-push/{composePath...}", h.pushGitStack)
	mux.HandleFunc("POST /bindings/{id}/orphan/{composePath...}", h.resolveGitOrphan)
	mux.HandleFunc("POST /bindings", h.createBinding)
	mux.HandleFunc("PUT /bindings/{id}/policy", h.updateBindingPolicy)
	mux.HandleFunc("PUT /bindings/{id}/compose-selection", h.updateBindingComposeSelection)
	mux.HandleFunc("PUT /bindings/{id}/automation", h.updateBindingAutomation)
	mux.HandleFunc("GET /bindings/{id}/deployments", h.listBindingDeployments)
	mux.HandleFunc("POST /bindings/{id}/automation/run", h.runBindingAutomation)
	mux.HandleFunc("POST /bindings/{id}/exclusions", h.addBindingExclusion)
	mux.HandleFunc("POST /bindings/{id}/exclusions/batch", h.addBindingExclusions)
	mux.HandleFunc("POST /bindings/{id}/inclusions/batch", h.addBindingInclusions)
	mux.HandleFunc("DELETE /bindings/{id}", h.deleteBinding)
	mux.HandleFunc("POST /bindings/{id}/preview/{direction}", h.previewBinding)
	mux.HandleFunc("POST /bindings/{id}/compare/{direction}", h.compareBindingFile)
	mux.HandleFunc("POST /bindings/{id}/export", h.exportBinding)
	mux.HandleFunc("POST /bindings/{id}/import", h.importBinding)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if isMemoryIntensiveGitRequest(r) {
			releaseMemory := observeGitMemory(r.Method + " " + r.URL.Path)
			defer releaseMemory()
		}
		mux.ServeHTTP(w, r)
	})
}

func (h *HTTPHandler) pushGitStack(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	result, err := h.service.PushGitStack(r.Context(), r.PathValue("id"), r.PathValue("composePath"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) resolveGitOrphan(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input OrphanActionInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.service.ResolveGitOrphan(r.Context(), r.PathValue("id"), r.PathValue("composePath"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) listGitStackStatuses(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	rows, err := h.service.ListGitStackStatusViews(r.URL.Query().Get("host"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *HTTPHandler) pauseGitStackAutomation(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input GitStackPauseInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.SetGitStackAutomationPause(r.PathValue("id"), r.PathValue("composePath"), input.Paused)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) updateBindingComposeSelection(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input BindingComposeSelectionInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.UpdateBindingComposeSelection(r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) listBindingDeployments(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	rows, err := h.service.ListBindingDeployments(r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func isMemoryIntensiveGitRequest(r *http.Request) bool {
	if r.Method == http.MethodGet || strings.HasSuffix(r.URL.Path, "/automation/run") {
		return false
	}
	for _, marker := range []string{"/preview/", "/compare/", "/import", "/export", "/fetch", "/pull", "/push", "/stack-push/", "/orphan/", "/repositories"} {
		if strings.Contains(r.URL.Path, marker) {
			return true
		}
	}
	return false
}

func (h *HTTPHandler) addBindingExclusion(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input BindingExclusionInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.AddBindingExclusion(r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) addBindingExclusions(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input BindingExclusionsInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.AddBindingExclusions(r.PathValue("id"), input.Entries)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) addBindingInclusions(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input BindingInclusionsInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.AddBindingInclusions(r.PathValue("id"), input.Paths)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) updateBindingPolicy(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input BindingPolicyInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.UpdateBindingPolicy(r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) updateRepositoryPolicy(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input RepositoryPolicyInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.UpdateRepositoryPolicy(r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) updateBindingAutomation(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input BindingAutomationInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.UpdateBindingAutomation(r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) runBindingAutomation(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	result, err := h.service.RunBindingAutoSync(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) listStackTargets(w http.ResponseWriter, _ *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	rows, err := h.service.ListStackTargets()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *HTTPHandler) listBindings(w http.ResponseWriter, _ *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	rows, err := h.service.ListBindings()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *HTTPHandler) createBinding(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input BindingInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.CreateBindingContext(r.Context(), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (h *HTTPHandler) deleteBinding(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	if err := h.service.DeleteBinding(r.PathValue("id"), r.URL.Query().Get("forget") == "true"); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) previewBinding(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	input, ok := decodeOptionalTransferInput(w, r)
	if !ok {
		return
	}
	preview, err := h.service.PreviewBinding(r.PathValue("id"), r.PathValue("direction"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (h *HTTPHandler) compareBindingFile(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input ComparisonInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	comparison, err := h.service.CompareBindingFile(r.PathValue("id"), r.PathValue("direction"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comparison)
}

func (h *HTTPHandler) exportBinding(w http.ResponseWriter, r *http.Request) {
	h.bindingTransfer(w, r, h.service.ExportBinding)
}

func (h *HTTPHandler) importBinding(w http.ResponseWriter, r *http.Request) {
	h.bindingTransfer(w, r, h.service.ImportBinding)
}

func (h *HTTPHandler) bindingTransfer(w http.ResponseWriter, r *http.Request, action func(context.Context, string, TransferInput) (TransferResult, error)) {
	if !h.requireEnabled(w) {
		return
	}
	input, ok := decodeOptionalTransferInput(w, r)
	if !ok {
		return
	}
	result, err := action(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func decodeOptionalTransferInput(w http.ResponseWriter, r *http.Request) (TransferInput, bool) {
	var input TransferInput
	err := decodeJSON(r, &input)
	if err != nil && !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return input, false
	}
	return input, true
}

func (h *HTTPHandler) listRepositories(w http.ResponseWriter, _ *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	rows, err := h.service.ListRepositories()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *HTTPHandler) createRepository(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input RepositoryInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.CreateRepository(r.Context(), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (h *HTTPHandler) createGitHubRepository(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input GitHubRepositoryInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.service.CreateGitHubRepository(r.Context(), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (h *HTTPHandler) repositoryStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	status, err := h.service.RepositoryStatus(r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *HTTPHandler) fetchRepository(w http.ResponseWriter, r *http.Request) {
	h.repositoryAction(w, r, h.service.FetchRepository)
}

func (h *HTTPHandler) pullRepository(w http.ResponseWriter, r *http.Request) {
	h.repositoryAction(w, r, h.service.PullRepository)
}

func (h *HTTPHandler) pushRepository(w http.ResponseWriter, r *http.Request) {
	h.repositoryAction(w, r, h.service.PushRepository)
}

func (h *HTTPHandler) repositoryAction(w http.ResponseWriter, r *http.Request, action func(context.Context, string) (RepositoryGitStatus, error)) {
	if !h.requireEnabled(w) {
		return
	}
	status, err := action(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *HTTPHandler) repositoryOperations(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.service.ListRepositoryOperations(r.PathValue("id"), limit)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *HTTPHandler) deleteRepository(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	if err := h.service.DeleteRepository(r.PathValue("id")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":                 h.service.Enabled(),
		"phase":                   "safe_automation",
		"repositorySyncAvailable": h.service.Enabled(),
		"stackSyncAvailable":      h.service.Enabled(),
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
		writeAPIError(w, http.StatusNotFound, "Git resource not found")
	case strings.Contains(strings.ToLower(err.Error()), "unique constraint"):
		writeAPIError(w, http.StatusConflict, "A Git resource with this name already exists")
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
