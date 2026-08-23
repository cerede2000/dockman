package files

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/dockyaml"
	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

func TestSaveUsesCreateCompatibleWriteMode(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	srv := New(func(host, alias string) (filesystem.FileSystem, error) {
		require.Equal(t, "remote", host)
		require.Equal(t, "compose", alias)
		return filesystem.NewLocal(root), nil
	}, nil)

	// Editor saves use create=false. The destination should still be opened
	// with CREATE so SFTP servers that require WRITE|CREATE|TRUNC accept it.
	err := srv.Save("compose/new-file.yml", "remote", false, bytes.NewBufferString("services: {}\n"))
	require.NoError(t, err)

	contents, err := os.ReadFile(filepath.Join(root, "new-file.yml"))
	require.NoError(t, err)
	require.Equal(t, "services: {}\n", string(contents))

	err = srv.Save("compose/new-file.yml", "remote", false, bytes.NewBufferString("services:\n  app: {}\n"))
	require.NoError(t, err)

	contents, err = os.ReadFile(filepath.Join(root, "new-file.yml"))
	require.NoError(t, err)
	require.Equal(t, "services:\n  app: {}\n", string(contents))
}

func TestSaveIfRevisionRejectsAnObsoleteEditor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	srv := New(func(_, _ string) (filesystem.FileSystem, error) { return filesystem.NewLocal(root), nil }, nil)
	path := filepath.Join(root, "compose.yml")
	require.NoError(t, os.WriteFile(path, []byte("version: one\n"), 0o644))

	revision, err := srv.Revision("compose/compose.yml", "local")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("version: git\n"), 0o644))

	current, err := srv.SaveIfRevision("compose/compose.yml", "local", revision, bytes.NewBufferString("version: editor\n"))
	require.ErrorIs(t, err, ErrStaleFile)
	require.NotEqual(t, revision, current)
	contents, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "version: git\n", string(contents))
}

func TestDeleteGuardRunsBeforeFilesystemRemoval(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "linked"), 0o755))
	srv := New(func(_, _ string) (filesystem.FileSystem, error) { return filesystem.NewLocal(root), nil }, nil)
	srv.ConfigureDeleteGuard(func(host, path string) error {
		require.Equal(t, "local", host)
		require.Equal(t, "compose/linked", path)
		return errors.New("protected Folder Link")
	})
	require.ErrorContains(t, srv.Delete("compose/linked", "local"), "protected Folder Link")
	require.DirExists(t, filepath.Join(root, "linked"))
}

func TestList(t *testing.T) {
	// todo
	//structure, err := CreateRandomDirStructure(5)
	//require.NoErrorf(t, err, "Error creating random folder structure")
	//defer os.RemoveAll(structure)
	//
	//fileSrv := New("../../", "", 1000, 1000, func() string {
	//	return docker.LocalClient
	//})
	//
	//list, err := fileSrv.List("")
	//require.NoErrorf(t, err, "Error listing files")
	//
	//t.Log(list)
}

func TestTemplateRead(t *testing.T) {
	root := "./tmp/compose"
	root, err := filepath.Abs(root)
	require.NoError(t, err)

	lfs := filesystem.NewLocal(root)

	srv := New(func(host, alias string) (filesystem.FileSystem, error) {
		return lfs, nil
	}, nil)

	tmpls, err := srv.GetTemplates("compose", "test")
	require.NoError(t, err)

	for _, tmpl := range tmpls {
		for ke := range tmpl.vars {
			delete(tmpl.vars, ke)
			prefix := strings.TrimPrefix(ke, ".")
			tmpl.vars[prefix] = ".dyn" + ke
		}

		err := srv.WriteTemplate("test", "compose/base", &tmpl)
		require.NoError(t, err)
		break
	}
}

func sortNames(srv *Service, entries []Entry) []string {
	slices.SortFunc(entries, func(a, b Entry) int {
		return srv.sortFiles(&a, &b, "local")
	})
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.fullpath
	}
	return names
}

func dirEntry(name string) Entry  { return Entry{fullpath: name, isDir: true} }
func fileEntry(name string) Entry { return Entry{fullpath: name, isDir: false} }

// TestSortFoldersFirst covers the VS Code-style ordering: folders first, then
// files, each case-insensitive, with dotfiles floating to the top of their own
// group rather than forming a separate group above the directories.
func TestSortFoldersFirst(t *testing.T) {
	srv := &Service{dockYml: func(string) *dockyaml.DockmanYaml { return &dockyaml.DockmanYaml{} }}

	// Same set as the bytebot repo root, shuffled.
	input := []Entry{
		fileEntry("README.md"), dirEntry("docker"), fileEntry(".gitignore"), dirEntry("static"),
		dirEntry(".github"), fileEntry("LICENSE"), dirEntry("helm"), dirEntry(".git"),
		dirEntry("packages"), fileEntry(".prettierignore"), dirEntry("docs"),
	}

	want := []string{
		// directories first (dotfolders float to the top), case-insensitive
		".git", ".github", "docker", "docs", "helm", "packages", "static",
		// then files (dotfiles float to the top), case-insensitive
		".gitignore", ".prettierignore", "LICENSE", "README.md",
	}
	require.Equal(t, want, sortNames(srv, input))
}

// TestSortComposePinnedAndCase covers the Dockman-specific extras kept on top
// of the VS Code ordering: pinned files win outright, compose/yaml files
// surface first within the files group, and case is ignored ("Backups" < "data").
func TestSortComposePinnedAndCase(t *testing.T) {
	srv := &Service{dockYml: func(string) *dockyaml.DockmanYaml {
		return &dockyaml.DockmanYaml{PinnedFiles: map[string]int{"notes.md": 0}}
	}}

	input := []Entry{
		fileEntry("app.env"), dirEntry("data"), fileEntry("values.yaml"), dirEntry("Backups"),
		fileEntry("compose.yaml"), fileEntry("notes.md"), fileEntry(".env"),
	}

	want := []string{
		"notes.md",        // pinned wins over everything
		"Backups", "data", // folders, case-insensitive
		"compose.yaml",    // files: compose first
		"values.yaml",     // then other yaml
		".env", "app.env", // then remaining files, case-insensitive (dot floats)
	}
	require.Equal(t, want, sortNames(srv, input))
}

func CreateRandomDirStructure(rootDir string, maxDepth int) (string, error) {
	err := os.MkdirAll(rootDir, 0755)
	if err != nil {
		return "", err
	}

	numFiles := rand.Intn(11) + 5

	for i := 0; i < numFiles; i++ {
		depth := rand.Intn(maxDepth + 1)

		dirPath := rootDir
		for d := 0; d < depth; d++ {
			dirPath = filepath.Join(dirPath, fmt.Sprintf("dir_%d", rand.Intn(100)))
		}

		err = os.MkdirAll(dirPath, 0755)
		if err != nil {
			return rootDir, err
		}

		fileName := fmt.Sprintf("file_%d.txt", rand.Intn(1000))
		filePath := filepath.Join(dirPath, fileName)

		content := fmt.Sprintf("Random file created at depth %d\n", depth)
		err = os.WriteFile(filePath, []byte(content), 0644)
		if err != nil {
			return rootDir, err
		}
	}

	return rootDir, nil
}

// The destination filesystem used to be discarded and the copy written through
// the SOURCE one. Copying between two aliases reported success, created nothing
// where it was asked to, and left a stray file in the source root.
func TestCopyWritesIntoTheDestinationAlias(t *testing.T) {
	t.Parallel()

	rootA, rootB := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(rootA, "source.yml"), []byte("from alias A\n"), 0o640))
	srv := New(func(_, alias string) (filesystem.FileSystem, error) {
		switch alias {
		case "aliasA":
			return filesystem.NewLocal(rootA), nil
		case "aliasB":
			return filesystem.NewLocal(rootB), nil
		}
		return nil, os.ErrNotExist
	}, nil)

	require.NoError(t, srv.Copy("aliasA/source.yml", "aliasB/dest.yml", "local", false))

	copied, err := os.ReadFile(filepath.Join(rootB, "dest.yml"))
	require.NoError(t, err, "the copy must land in the destination alias")
	require.Equal(t, "from alias A\n", string(copied))

	require.NoFileExists(t, filepath.Join(rootA, "dest.yml"), "and never in the source alias")

	// the source's mode travels with it: a copied script stays executable, and
	// a copied config never becomes one
	info, err := os.Stat(filepath.Join(rootB, "dest.yml"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

func TestCopyWithinOneAliasStillWorks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "compose.yml"), []byte("services: {}\n"), 0o644))
	srv := New(func(_, _ string) (filesystem.FileSystem, error) { return filesystem.NewLocal(root), nil }, nil)

	require.NoError(t, srv.Copy("compose/compose.yml", "compose/nested/copy.yml", "local", false))
	copied, err := os.ReadFile(filepath.Join(root, "nested", "copy.yml"))
	require.NoError(t, err)
	require.Equal(t, "services: {}\n", string(copied))
}

// Deletion has been guarded against removing a Folder Link root all along.
// Renaming one has the same consequence - the link points at a path that no
// longer exists, and every synchronized file then reads as deleted locally -
// and had no protection at all.
func TestRenameIsRefusedWhenAGuardVetoesIt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "linked"), 0o755))
	srv := New(func(_, _ string) (filesystem.FileSystem, error) { return filesystem.NewLocal(root), nil }, nil)

	var inspected []string
	srv.ConfigureRenameGuard(func(_, path string) error {
		inspected = append(inspected, path)
		if strings.HasSuffix(path, "linked") {
			return errors.New("this directory is a Git Folder Link root")
		}
		return nil
	})

	err := srv.Rename("compose/linked", "compose/renamed", "local")
	require.ErrorContains(t, err, "Folder Link root")
	require.Equal(t, []string{"compose/linked"}, inspected, "the guard must see the path being renamed away")
	require.DirExists(t, filepath.Join(root, "linked"), "nothing may move once the guard refused")
	require.NoDirExists(t, filepath.Join(root, "renamed"))

	// anything the guard allows still renames normally
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644))
	require.NoError(t, srv.Rename("compose/notes.txt", "compose/notes-renamed.txt", "local"))
	require.FileExists(t, filepath.Join(root, "notes-renamed.txt"))
}

// Every variable in a template's filename has to be substituted. Replacing in
// the original name each time meant only the last one survived, and a template
// named "$stack$/$service$.yml" created a directory literally called "$stack$".
func TestTemplateFilenameSubstitutesEveryVariable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, TemplateFolder), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "base"), 0o755))
	body := "{{define \"$stack$/$service$.yml\"}}services:\n  {{.service}}:\n    image: alpine\n{{end}}"
	require.NoError(t, os.WriteFile(filepath.Join(root, TemplateFolder, "two-vars.yml"), []byte(body), 0o644))

	srv := New(func(_, _ string) (filesystem.FileSystem, error) { return filesystem.NewLocal(root), nil }, nil)
	tmpls, err := srv.GetTemplates("compose", "local")
	require.NoError(t, err)
	require.Len(t, tmpls, 1)

	tmpls[0].vars["$stack$"] = "web"
	tmpls[0].vars["$service$"] = "api"
	tmpls[0].vars["service"] = "api"
	require.NoError(t, srv.WriteTemplate("local", "compose/base", &tmpls[0]))

	require.FileExists(t, filepath.Join(root, "base", "web", "api.yml"))
	require.NoDirExists(t, filepath.Join(root, "base", "$stack$"), "no variable may survive into a real path")

	contents, err := os.ReadFile(filepath.Join(root, "base", "web", "api.yml"))
	require.NoError(t, err)
	require.Contains(t, string(contents), "api:")
}

// A template that names a variable it was not given must refuse rather than
// write a path with a placeholder still in it.
func TestTemplateRefusesAMissingVariable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, TemplateFolder), 0o755))
	body := "{{define \"$stack$/app.yml\"}}services: {}\n{{end}}"
	require.NoError(t, os.WriteFile(filepath.Join(root, TemplateFolder, "one-var.yml"), []byte(body), 0o644))

	srv := New(func(_, _ string) (filesystem.FileSystem, error) { return filesystem.NewLocal(root), nil }, nil)
	tmpls, err := srv.GetTemplates("compose", "local")
	require.NoError(t, err)
	require.Len(t, tmpls, 1)

	require.ErrorContains(t, srv.WriteTemplate("local", "compose/base", &tmpls[0]), "$stack$")
	require.NoDirExists(t, filepath.Join(root, "base"))
}
