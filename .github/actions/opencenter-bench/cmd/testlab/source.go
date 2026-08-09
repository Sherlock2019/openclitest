package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/source"
)

const openCLIRepository = source.Default

// handleSourceClone fetches a copy of the source and says where it is.
//
// Separate from installing. "Install OpenCLI" builds a binary and points the
// bench at it, which is a change to what gets tested; this only puts the code
// on disk so it can be read. Somebody looking at a failure wants to open the
// file the bench complained about, and that should not require installing
// anything or knowing where a sandbox went.
func (c *console) handleSourceClone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var incoming struct {
		Repository string `json:"repository"`
		Ref        string `json:"ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(incoming.Repository) == "" {
		incoming.Repository = source.Default
	}

	// Long enough for a cold clone of a real repository over ssh, short enough
	// that a wrong URL answers while somebody is still looking at the field.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	checkout, err := source.Clone(ctx, incoming.Repository, incoming.Ref)
	if err != nil {
		// Not a 500. The usual cause is a repository that does not exist, a
		// branch that does not, or no key for it — all answers about the input.
		writeJSON(w, http.StatusOK, map[string]any{
			"repository": incoming.Repository, "ref": incoming.Ref,
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, checkout)
}

// handleSourceBranches lists what a repository has, so the page can offer the
// branches instead of asking somebody to remember one.
//
// The repository comes from the query, because the field on the page is
// editable: a fork is the ordinary case for testing a change before it is
// merged. What may be asked for is checked in internal/source.
func (c *console) handleSourceBranches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	repository := r.URL.Query().Get("repo")
	if repository == "" {
		repository = source.Default
	}
	if err := source.ValidateRepository(repository); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Long enough for a cold SSH connection, short enough that a wrong URL
	// tells you so while you are still looking at the field.
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	refs, err := source.List(ctx, repository)
	if err != nil {
		// Not a 500: the usual cause is a repository that does not exist or
		// that this machine has no key for, and that is an answer about the
		// input rather than a fault in the console.
		writeJSON(w, http.StatusOK, map[string]any{
			"repository": repository, "default": "main",
			"branches": []string{}, "tags": []string{}, "error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repository": refs.Repository, "default": "main",
		"branches": refs.Branches, "tags": refs.Tags,
	})
}
