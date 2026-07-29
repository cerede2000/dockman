package gitsync

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeCommitIdentity(t *testing.T) {
	name, email, err := normalizeCommitIdentity("  Homelab Bot  ", "bot@example.test")
	require.NoError(t, err)
	require.Equal(t, "Homelab Bot", name)
	require.Equal(t, "bot@example.test", email)

	_, _, err = normalizeCommitIdentity("Bad\nActor", "bot@example.test")
	require.ErrorContains(t, err, "author name")
	_, _, err = normalizeCommitIdentity("Bot", "not-an-email")
	require.ErrorContains(t, err, "author email")
}

func TestProvenanceValueRemovesControlCharactersAndBoundsLength(t *testing.T) {
	require.Equal(t, `"linebreak"`, provenanceValue("line\nbreak"))
	require.LessOrEqual(t, len([]rune(provenanceValue(string(make([]byte, 200))))), 122)
}
