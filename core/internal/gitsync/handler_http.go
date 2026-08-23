package gitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	mux.HandleFunc("GET /repositories/{id}/webhook", h.repositoryWebhook)
	mux.HandleFunc("PUT /repositories/{id}/webhook", h.configureRepositoryWebhook)
	mux.HandleFunc("GET /repositories/{id}/status", h.repositoryStatus)
	mux.HandleFunc("POST /repositories/{id}/fetch", h.fetchRepository)
	mux.HandleFunc("POST /repositories/{id}/pull", h.pullRepository)
	mux.HandleFunc("POST /repositories/{id}/push", h.pushRepository)
	mux.HandleFunc("POST /repositories/{id}/reset-to-remote", h.resetRepositoryToRemote)
	mux.HandleFunc("GET /repositories/{id}/stack-catalog", h.repositoryStackCatalog)
	mux.HandleFunc("GET /repositories/{id}/operations", h.repositoryOperations)
	mux.HandleFunc("DELETE /repositories/{id}", h.deleteRepository)
	mux.HandleFunc("GET /stack-targets", h.listStackTargets)
	mux.HandleFunc("GET /bindings", h.listBindings)
	mux.HandleFunc("GET /stack-statuses", h.listGitStackStatuses)
	mux.HandleFunc("POST /tracked-files", h.gitTrackedFiles)
	mux.HandleFunc("PUT /file-tracking", h.setGitFileTracking)
	mux.HandleFunc("PUT /bindings/{id}/stack-status/{composePath...}", h.pauseGitStackAutomation)
	mux.HandleFunc("POST /bindings/{id}/stack-select/{composePath...}", h.enableGitStackSynchronization)
	mux.HandleFunc("POST /bindings/{id}/stack-push/{composePath...}", h.pushGitStack)
	mux.HandleFunc("POST /bindings/{id}/stack-sync/{composePath...}", h.syncGitStack)
	mux.HandleFunc("POST /bindings/{id}/orphan/{composePath...}", h.resolveGitOrphan)
	mux.HandleFunc("POST /bindings/{id}/local-deletion/{composePath...}", h.resolveLocalStackDeletion)
	mux.HandleFunc("GET /bindings/{id}/local-deletion/{composePath...}", h.listLocalStackDeletions)
	mux.HandleFunc("GET /bindings/{id}/folder-deletion", h.inspectFolderLinkDeletion)
	mux.HandleFunc("POST /bindings/{id}/folder-deletion", h.deleteFolderLinkRoot)
	mux.HandleFunc("POST /bindings", h.createBinding)
	mux.HandleFunc("PUT /bindings/{id}/policy", h.updateBindingPolicy)
	mux.HandleFunc("POST /bindings/{id}/policy-tree", h.bindingPolicyTree)
	mux.HandleFunc("POST /bindings/{id}/refresh-compose", h.refreshBindingComposeCatalog)
	mux.HandleFunc("PUT /bindings/{id}/compose-selection", h.updateBindingComposeSelection)
	mux.HandleFunc("PUT /bindings/{id}/automation", h.updateBindingAutomation)
	mux.HandleFunc("PUT /bindings/{id}/automation/pause", h.pauseBindingAutomation)
	mux.HandleFunc("GET /bindings/{id}/deployments", h.listBindingDeployments)
	mux.HandleFunc("GET /bindings/{id}/operations", h.bindingOperations)
	mux.HandleFunc("GET /bindings/{id}/backups", h.listBindingBackups)
	mux.HandleFunc("GET /bindings/{id}/commits", h.listBindingCommits)
	mux.HandleFunc("POST /bindings/{id}/rollback-preview", h.previewBindingCommitRollback)
	mux.HandleFunc("POST /bindings/{id}/rollback-compare/{path...}", h.compareBindingCommitRollback)
	mux.HandleFunc("POST /bindings/{id}/rollback", h.applyBindingCommitRollback)
	mux.HandleFunc("GET /bindings/{id}/backups/{backupId}/download", h.downloadBindingBackup)
	mux.HandleFunc("DELETE /bindings/{id}/backups/{backupId}", h.deleteBindingBackup)
	mux.HandleFunc("GET /bindings/{id}/backups/{backupId}/restore-preview", h.previewBindingBackupRestore)
	mux.HandleFunc("POST /bindings/{id}/backups/{backupId}/restore", h.restoreBindingBackup)
	mux.HandleFunc("POST /bindings/{id}/automation/run", h.runBindingAutomation)
	mux.HandleFunc("POST /bindings/{id}/automation/reset-state", h.resetBindingAutomationState)
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

func (h *HTTPHandler) repositoryWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	result, err := h.service.RepositoryWebhook(r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) configureRepositoryWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input RepositoryWebhookInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.service.ConfigureRepositoryWebhook(r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) repositoryStackCatalog(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	result, err := h.service.RepositoryStackCatalog(r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) pushGitStack(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	result, err := h.service.PushGitStackAndResume(r.Context(), r.PathValue("id"), r.PathValue("composePath"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) syncGitStack(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	result, err := h.service.SyncGitStackNow(r.Context(), r.PathValue("id"), r.PathValue("composePath"))
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

func (h *HTTPHandler) resolveLocalStackDeletion(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input LocalDeletionActionInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.service.ResolveLocalStackDeletion(r.Context(), r.PathValue("id"), r.PathValue("composePath"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) listLocalStackDeletions(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	result, err := h.service.ListLocalStackDeletions(r.PathValue("id"), r.PathValue("composePath"))
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

func (h *HTTPHandler) gitTrackedFiles(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input GitTrackedFilesInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.service.GitTrackedFiles(input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) setGitFileTracking(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input GitFileTrackingInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.service.SetGitFileTracking(input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
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
	var row GitStackStatusView
	var pushed bool
	var err error
	if input.Paused {
		row, err = h.service.SetGitStackAutomationPause(r.PathValue("id"), r.PathValue("composePath"), true)
	} else {
		row, pushed, err = h.service.ResumeGitStackAutomation(r.Context(), r.PathValue("id"), r.PathValue("composePath"))
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	action := "pause"
	if !input.Paused {
		action = "resume"
	}
	message := ""
	if pushed {
		message = "Local changes pushed before resuming automatic synchronization"
	}
	h.service.recordActivity(ActivityRecord{RepositoryID: row.RepositoryID, BindingID: row.BindingID, ComposePath: row.ComposePath,
		Type: "stack_automation", Trigger: "manual", Details: ActivityDetails{Action: action, Message: message, Paths: []string{row.ComposePath}}})
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) enableGitStackSynchronization(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	row, err := h.service.EnableGitStackSynchronization(r.PathValue("id"), r.PathValue("composePath"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.service.recordActivity(ActivityRecord{RepositoryID: row.RepositoryID, BindingID: row.BindingID, ComposePath: row.ComposePath,
		Type: "stack_selection", Trigger: "manual", Details: ActivityDetails{Action: "enable", Paths: []string{row.ComposePath}}})
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
	h.service.recordActivity(ActivityRecord{RepositoryID: row.RepositoryID, BindingID: row.ID, Type: "stack_selection",
		Trigger: "manual", Details: ActivityDetails{Action: "update", Paths: row.SelectedComposePaths}})
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) refreshBindingComposeCatalog(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	row, err := h.service.RefreshBindingComposeCatalog(r.PathValue("id"))
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

func (h *HTTPHandler) bindingOperations(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	rows, err := h.service.ListBindingOperations(r.PathValue("id"), limit, offset)
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
	for _, marker := range []string{"/preview/", "/compare/", "/rollback", "/import", "/export", "/fetch", "/pull", "/push", "/stack-push/", "/stack-sync/", "/orphan/", "/local-deletion/", "/file-tracking", "/repositories"} {
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
	h.service.recordActivity(ActivityRecord{RepositoryID: row.RepositoryID, BindingID: row.ID, Type: "policy_update",
		Trigger: "manual", Details: ActivityDetails{Action: "update"}})
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) bindingPolicyTree(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input BindingPolicyTreeInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	view, err := h.service.BindingPolicyTree(r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
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
	action := "disable"
	if input.Enabled {
		action = "enable"
	}
	h.service.recordActivity(ActivityRecord{RepositoryID: row.RepositoryID, BindingID: row.ID, Type: "automation_config",
		Trigger: "manual", Details: ActivityDetails{Action: action}})
	writeJSON(w, http.StatusOK, row)
}

func (h *HTTPHandler) pauseBindingAutomation(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input BindingAutomationPauseInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.service.SetBindingAutomationPause(r.Context(), r.PathValue("id"), input.Paused)
	action := "resume"
	if input.Paused {
		action = "pause"
	}
	if result.Binding.ID != "" {
		h.service.recordActivity(ActivityRecord{RepositoryID: result.Binding.RepositoryID, BindingID: result.Binding.ID, Type: "automation_pause",
			Trigger: "manual", Details: ActivityDetails{Action: action}})
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) resetBindingAutomationState(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	result, err := h.service.ResetBindingAutomationState(r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) runBindingAutomation(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	result, err := h.service.RunBindingAutoSyncNow(r.Context(), r.PathValue("id"))
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
	h.service.recordActivity(ActivityRecord{RepositoryID: row.RepositoryID, BindingID: row.ID, Type: "binding_create",
		Trigger: "manual", Details: ActivityDetails{Action: "create", Paths: row.SelectedComposePaths}})
	writeJSON(w, http.StatusCreated, row)
}

func (h *HTTPHandler) deleteBinding(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	binding, err := h.service.store.GetBinding(r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	forget := r.URL.Query().Get("forget") == "true"
	if err = h.service.DeleteBinding(r.PathValue("id"), forget); err != nil {
		writeServiceError(w, err)
		return
	}
	h.service.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID,
		Type: "binding_delete", Trigger: "manual", Details: ActivityDetails{Action: map[bool]string{true: "unlink_and_forget", false: "unlink"}[forget]}})
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) inspectFolderLinkDeletion(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	result, err := h.service.InspectFolderLinkDeletion(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) deleteFolderLinkRoot(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input FolderLinkDeletionInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.service.DeleteFolderLinkRoot(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
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
		var missingBranch *RemoteBranchMissingError
		if errors.As(err, &missingBranch) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": missingBranch.Error(), "code": "remote_branch_missing",
				"branch": missingBranch.Branch, "sourceBranch": missingBranch.SourceBranch,
				"canCreate":            missingBranch.CanCreate,
				"canCreateFromDefault": missingBranch.CanCreateFromDefault,
				"canCreateEmpty":       missingBranch.CanCreateEmpty,
			})
			return
		}
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

func (h *HTTPHandler) resetRepositoryToRemote(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input struct {
		Confirmation string `json:"confirmation"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if input.Confirmation != typedConfirmationText {
		writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("type %q to confirm", typedConfirmationText))
		return
	}
	status, err := h.service.ResetRepositoryToRemote(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
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
		"historyRetentionDays":    h.service.historyRetentionDays,
		"backupRetentionDays":     h.service.backupRetentionDays,
		"backupRetentionCount":    gitBackupRetention,
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
