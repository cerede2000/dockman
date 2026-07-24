package gitsync

import (
	"io"
	"net/http"
	"strconv"
)

func (h *HTTPHandler) listBindingBackups(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.service.ListBindingBackups(r.PathValue("id"), limit)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *HTTPHandler) listBindingCommits(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.service.ListBindingCommits(r.PathValue("id"), limit)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *HTTPHandler) previewBindingCommitRollback(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input CommitRollbackInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	preview, err := h.service.PreviewCommitRollback(r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (h *HTTPHandler) compareBindingCommitRollback(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input CommitRollbackInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	comparison, err := h.service.CompareCommitRollbackFile(r.PathValue("id"), input, r.PathValue("path"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comparison)
}

func (h *HTTPHandler) applyBindingCommitRollback(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input CommitRollbackInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.service.ApplyCommitRollback(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) downloadBindingBackup(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	handle, name, err := h.service.OpenBackup(r.PathValue("id"), r.PathValue("backupId"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	defer handle.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
	_, _ = io.Copy(w, handle)
}

func (h *HTTPHandler) deleteBindingBackup(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	if err := h.service.DeleteBackup(r.PathValue("id"), r.PathValue("backupId")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) previewBindingBackupRestore(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	preview, err := h.service.PreviewBackupRestore(r.PathValue("id"), r.PathValue("backupId"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (h *HTTPHandler) restoreBindingBackup(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnabled(w) {
		return
	}
	var input BackupRestoreInput
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.service.RestoreBackup(r.Context(), r.PathValue("id"), r.PathValue("backupId"), input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
