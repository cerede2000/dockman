package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

func TestComposeFileExistsRejectsStaleDockerLabelPath(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "substacks", "current"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "substacks", "current", "compose.yml"), []byte("services: {}\n"), 0o600))

	service := NewComposeTerminal("local", nil, func(filename, _ string) (Host, error) {
		return Host{
			Fs:      filesystem.NewLocal(root),
			Relpath: strings.TrimPrefix(filename, "compose/"),
		}, nil
	}, nil)

	exists, err := service.ComposeFileExists("compose/substacks/current/compose.yml")
	require.NoError(t, err)
	require.True(t, exists)

	exists, err = service.ComposeFileExists("compose/substacks/deleted/compose.yml")
	require.NoError(t, err)
	require.False(t, exists, "containers may retain a compose path after its stack file was deleted")

	exists, err = service.ComposeFileExists("compose/substacks/current")
	require.NoError(t, err)
	require.False(t, exists, "a directory cannot be indexed as a compose file")
}
