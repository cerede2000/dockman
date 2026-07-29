package gitsync

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func repositoryCommitSignature(repository Repository, when time.Time) *object.Signature {
	name, email, err := normalizeCommitIdentity(repository.CommitAuthorName, repository.CommitAuthorEmail)
	if err != nil {
		name, email = "Dockman Git Sync", "dockman@localhost.invalid"
	}
	return &object.Signature{Name: name, Email: email, When: when.UTC()}
}

func (s *Service) bindingCommitOptions(repository Repository, binding *StackBinding, when time.Time) *gitclient.CommitOptions {
	signature := repositoryCommitSignature(repository, when)
	return &gitclient.CommitOptions{Author: signature, Committer: signature}
}

func (s *Service) commitMessageWithProvenance(message string, binding *StackBinding) string {
	parts := []string{"instance=" + provenanceValue(s.commitInstance)}
	if binding != nil {
		parts = append(parts,
			"host="+provenanceValue(binding.Host),
			"binding="+provenanceValue(binding.UUID),
			"stack="+provenanceValue(binding.StackPath),
		)
	}
	return strings.TrimSpace(message) + "\n\nDockman-Origin: " + strings.Join(parts, "; ")
}

func provenanceValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	if value == "" {
		return "unknown"
	}
	const maxRunes = 120
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes])
	}
	return fmt.Sprintf("%q", value)
}
