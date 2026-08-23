package dockyaml

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func openDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skip("cannot count open descriptors on this platform")
	}
	return len(entries)
}

// GetYaml opens the configuration file and used to leak the handle on every
// path out, including the cache hit. It is read from getSortRank, which runs
// inside a sort comparator, so listing one directory of N entries leaked
// O(N log N) descriptors. The process ran out of them, and then nothing could
// open a file or accept a connection any more.
func TestReadingTheConfigurationLeaksNoDescriptor(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "local.dockman.yml"), []byte("searchLimit: 25\n"), 0o644))
	service := New(NewStore(dir))

	// prime the cache first, so the loop below exercises the early return that
	// used to leak just as surely as the parsing path
	require.Equal(t, 25, service.GetYaml("local").SearchLimit)

	before := openDescriptors(t)
	for range 200 {
		require.Equal(t, 25, service.GetYaml("local").SearchLimit)
	}
	require.LessOrEqual(t, openDescriptors(t)-before, 2, "reading the configuration must not accumulate open files")
}

func TestReadingTheContentsLeaksNoDescriptor(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "local.dockman.yml"), []byte("tabLimit: 3\n"), 0o644))
	service := New(NewStore(dir))

	before := openDescriptors(t)
	for range 200 {
		contents, err := service.GetContents("local")
		require.NoError(t, err)
		require.Contains(t, string(contents), "tabLimit")
	}
	require.LessOrEqual(t, openDescriptors(t)-before, 2)
}

// A file the operator never wrote still yields the defaults.
func TestMissingConfigurationFallsBackToDefaults(t *testing.T) {
	service := New(NewStore(t.TempDir()))
	config := service.GetYaml("local")
	require.Equal(t, defaultDockmanYaml.SearchLimit, config.SearchLimit)
	require.Equal(t, defaultDockmanYaml.TabLimit, config.TabLimit)
}
