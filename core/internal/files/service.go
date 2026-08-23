package files

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"text/template/parse"
	"time"

	"github.com/RA341/dockman/internal/dockyaml"
	"github.com/RA341/dockman/internal/files/utils"
	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/RA341/dockman/pkg/fileutil"
	"github.com/gabriel-vasile/mimetype"
	"github.com/sahilm/fuzzy"
)

type FSProvider func(host, alias string) (filesystem.FileSystem, error)
type DockyamlProvider func(host string) *dockyaml.DockmanYaml

type Service struct {
	Fs             FSProvider
	dockYml        DockyamlProvider
	templateFolder string
	changeNotifier func(host, path string)
	deleteGuard    func(host, path string) error
	renameGuard    func(host, path string) error
	editor         *editorState
}

func New(
	fs FSProvider,
	dockYml DockyamlProvider,
) *Service {
	return &Service{
		Fs:      fs,
		dockYml: dockYml,
		// todo load from env
		templateFolder: "templates",
		editor:         newEditorState(),
	}
}

func (s *Service) ConfigureChangeNotifier(notifier func(host, path string)) {
	s.changeNotifier = notifier
}

func (s *Service) ConfigureDeleteGuard(guard func(host, path string) error) {
	s.deleteGuard = guard
}

// ConfigureRenameGuard refuses a rename that would break something the rest of
// Dockman still points at. Deletion has been guarded all along; a rename can
// have the same consequence and had no protection at all.
func (s *Service) ConfigureRenameGuard(guard func(host, path string) error) {
	s.renameGuard = guard
}

func (s *Service) NotifyChange(host, path string) {
	s.NotifyChangeWithSession(host, path, "")
}

func (s *Service) NotifyChangeWithSession(host, path, session string) {
	if s.changeNotifier != nil {
		s.changeNotifier(host, path)
	}
	s.editor.publish(FileChange{Host: host, Path: filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))), Session: session})
}

// NotifyExternalChange informs open editors without marking an incoming Git
// synchronization as a new local modification.
func (s *Service) NotifyExternalChange(host, path string) {
	s.editor.publish(FileChange{Host: host, Path: filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))})
}

func (s *Service) DirtyEditorPaths(host string) []string { return s.editor.dirtyPaths(host) }

func (s *Service) Revision(filename, hostname string) (string, error) {
	fileSystem, resolved, _, err := s.LoadFs(filename, hostname)
	if err != nil {
		return "", err
	}
	reader, err := fileSystem.OpenFile(resolved, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer fileutil.Close(reader)
	return hashRevision(reader)
}

var ErrStaleFile = errors.New("file changed since it was opened")

func (s *Service) SaveIfRevision(filename, hostname, expected string, source io.Reader) (string, error) {
	hasExpectedRevision := strings.TrimSpace(expected) != ""
	if hasExpectedRevision {
		current, err := s.Revision(filename, hostname)
		if err != nil {
			return "", err
		}
		if current != strings.Trim(strings.TrimSpace(expected), `"`) {
			return current, ErrStaleFile
		}
	}
	if err := s.Save(filename, hostname, false, source); err != nil {
		return "", err
	}
	// Drag-and-drop uploads do not participate in editor concurrency control.
	// Avoid reading a potentially multi-gigabyte upload back into memory merely
	// to compute an ETag that those callers do not use.
	if !hasExpectedRevision {
		return "", nil
	}
	return s.Revision(filename, hostname)
}

type Entry struct {
	fullpath string
	isDir    bool
	children []Entry
}

func (s *Service) List(path string, hostname string) ([]Entry, error) {
	cliFs, relpath, _, err := s.LoadFs(path, hostname)
	if err != nil {
		return nil, err
	}

	topLevelEntries, err := cliFs.ReadDir(relpath)
	if err != nil {
		return nil, fmt.Errorf("failed to list files in compose root: %v", err)
	}

	result := make([]Entry, 0, len(topLevelEntries))
	for _, entry := range topLevelEntries {
		fullRelpath := filepath.Join(relpath, entry.Name())
		displayPath := filepath.Join(path, entry.Name())

		isDir := entry.IsDir()

		ele := Entry{
			fullpath: displayPath,
			isDir:    isDir,
		}

		if isDir {
			children, err := s.listFiles(cliFs, fullRelpath, displayPath, hostname)
			if err != nil {
				return nil, err
			}
			ele.children = children
		}

		result = append(result, ele)
	}

	slices.SortFunc(result, func(a, b Entry) int {
		return s.sortFiles(&a, &b, hostname)
	})

	return result, nil
}

func (s *Service) listFiles(
	cliFs filesystem.FileSystem,
	relDirpath string,
	displayPath string,
	hostname string,
) ([]Entry, error) {
	subEntries, err := cliFs.ReadDir(relDirpath)
	if err != nil {
		return nil, err
	}

	filesInSubDir := make([]Entry, 0, len(subEntries))
	for _, subEntry := range subEntries {
		join := filepath.Join(displayPath, subEntry.Name())
		filesInSubDir = append(filesInSubDir,
			Entry{
				fullpath: join,
				isDir:    subEntry.IsDir(),
				children: []Entry{},
			},
		)
	}

	slices.SortFunc(filesInSubDir, func(a, b Entry) int {
		return s.sortFiles(&a, &b, hostname)
	})

	return filesInSubDir, nil
}

func (s *Service) Create(filename string, dir bool, hostname string) error {
	cliFs, filename, _, err := s.LoadFs(filename, hostname)
	if err != nil {
		return err
	}

	if dir {
		return cliFs.MkdirAll(filename, os.ModePerm)
	}

	baseDir := filepath.Dir(filename)
	if err = cliFs.MkdirAll(baseDir, 0755); err != nil {
		return err
	}

	file, err := cliFs.OpenFile(
		filename,
		os.O_RDWR|os.O_CREATE,
		os.ModePerm,
	)
	if err != nil {
		return err
	}
	fileutil.Close(file)

	return nil
}

func (s *Service) Copy(source, dest, hostname string, isDir bool) error {
	if isDir {
		return fmt.Errorf("directory copying is unimplemented")
	}

	sourceFs, sourceFile, _, err := s.LoadFs(source, hostname)
	if err != nil {
		return err
	}

	// The destination filesystem used to be discarded and the copy written
	// through the SOURCE one. Copying between two aliases reported success,
	// created nothing where it was asked to, and left a stray file in the
	// source root instead.
	destFs, destFile, _, err := s.LoadFs(dest, hostname)
	if err != nil {
		return err
	}

	info, err := sourceFs.Stat(sourceFile)
	if err != nil {
		return err
	}

	sourceReader, err := sourceFs.OpenFile(sourceFile, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer fileutil.Close(sourceReader)

	destWriter, err := destFs.OpenFile(destFile, os.O_RDWR|os.O_TRUNC|os.O_CREATE, info.Mode().Perm())
	if err != nil {
		return err
	}

	if _, err = io.Copy(destWriter, sourceReader); err != nil {
		_ = destWriter.Close()
		return err
	}
	// Closing is what commits the write, and its error is the copy's error:
	// the SFTP client buffers, so a handle dropped without Close can silently
	// lose the tail of the file. Neither handle used to be closed at all.
	return destWriter.Close()
}

func (s *Service) Exists(filename string, hostname string) error {
	cliFs, filename, _, err := s.LoadFs(filename, hostname)
	if err != nil {
		return err
	}

	stat, err := cliFs.Stat(filename)
	if err != nil {
		return err
	}
	if stat.IsDir() {
		return fmt.Errorf("%s is a directory, cannot be opened", filename)
	}

	return nil
}

func (s *Service) Delete(filename string, hostname string) error {
	if s.deleteGuard != nil {
		if err := s.deleteGuard(hostname, filename); err != nil {
			return err
		}
	}
	sfCli, fullpath, _, err := s.LoadFs(filename, hostname)
	if err != nil {
		return err
	}
	return sfCli.RemoveAll(fullpath)
}

// Rename todo refactor this
func (s *Service) Rename(oldFileName, newFilename, hostname string) error {
	if s.renameGuard != nil {
		if err := s.renameGuard(hostname, oldFileName); err != nil {
			return err
		}
	}
	cliFs, oldFullPath, _, err := s.LoadFs(oldFileName, hostname)
	if err != nil {
		return err
	}

	_, newFullPath, _, err := s.LoadFs(newFilename, hostname)
	if err != nil {
		return err
	}

	oldFileName = filepath.ToSlash(oldFullPath)
	newFilename = filepath.ToSlash(newFullPath)

	return cliFs.Rename(oldFileName, newFilename)
}

type Template struct {
	name string
	vars map[string]string
}

const TemplateFolder = "templates"

func (s *Service) WriteTemplate(hostname string, dest string, tpl *Template) error {
	fsCli, rel, _, err := s.LoadFs(tpl.name, hostname)
	if err != nil {
		return err
	}
	contents, err := fsCli.ReadFile(rel)
	if err != nil {
		return err
	}

	destFsCli, destRel, _, err := s.LoadFs(dest, hostname)
	if err != nil {
		return err
	}

	const selfName = "sd"
	vf := template.New(selfName).Funcs(s.getTmplFuncs())
	tmpl, err := vf.Parse(string(contents))
	if err != nil {
		return err
	}

	for _, subTmpl := range tmpl.Templates() {
		filename := subTmpl.Name()
		if filename == selfName {
			continue
		}

		vars := parseFilename(filename)
		newFilename := filename
		for _, replaceVal := range vars {
			newVal := tpl.vars[replaceVal]
			if newVal == "" {
				return fmt.Errorf("template variable %s is missing",
					replaceVal,
				)
			}
			// Substitute into the name built so far. Replacing in the ORIGINAL
			// name each time meant only the last variable survived: a template
			// named "$stack$/$service$.yml" created a directory literally
			// called "$stack$" on the host.
			newFilename = strings.ReplaceAll(newFilename, replaceVal, newVal)
		}

		err := s.createTmplFile(
			tmpl,
			tpl.vars,
			destFsCli,
			destRel,
			filename,
			newFilename,
		)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) createTmplFile(
	tmpl *template.Template,
	data map[string]string,
	destFsCli filesystem.FileSystem,
	destRel string,
	tmplName string,
	filename string,
) error {
	fpath := destFsCli.Join(destRel, filename)

	err := destFsCli.MkdirAll(filepath.Dir(fpath), os.ModePerm)
	if err != nil {
		return err
	}

	file, err := destFsCli.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, os.ModePerm)
	if err != nil {
		return err
	}
	defer fileutil.Close(file)

	return tmpl.ExecuteTemplate(file, tmplName, data)
}

func parseFilename(filename string) []string {
	var vars []string

	acc := ""
	for _, ch := range filename {
		if ch == '$' {
			if acc != "" {
				acc = acc + string(ch) // add the last $
				vars = append(vars, acc)
				acc = ""
				continue
			}

			acc = acc + string(ch)
			continue
		}
		if acc != "" {
			acc = acc + string(ch)
		}
	}

	return vars
}

// custom functions for now
func (s *Service) getTmplFuncs() template.FuncMap {
	return template.FuncMap{}
}

func (s *Service) GetTemplates(fPath string, hostname string) ([]Template, error) {
	fsCli, rel, parsedAlias, err := s.LoadFs(fPath, hostname)
	if err != nil {
		return nil, err
	}
	// always root it to alias we dont need to check sub paths
	rel = ""

	templateBase := fsCli.Join(rel, TemplateFolder)
	templates, err := fsCli.ReadDir(templateBase)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var tmpls []Template

	for _, tpl := range templates {
		relBase := fsCli.Join(templateBase, tpl.Name())
		content, err := fsCli.ReadFile(relBase)
		if err != nil {
			return nil, err
		}

		trees, err := parse.Parse(
			"stack", string(content),
			"", "",
			s.getTmplFuncs(),
		)
		if err != nil {
			return nil, err
		}

		name := filepath.Join(parsedAlias, relBase)
		elems := Template{
			name: name,
			vars: make(map[string]string),
		}

		for key := range trees {
			vars := parseFilename(key)
			for _, v := range vars {
				elems.vars[v] = ""
			}

			tree := trees[key]
			// get var names in tmpl
			for _, node := range tree.Root.Nodes {
				if action, ok := node.(*parse.ActionNode); ok {
					cleanKey := strings.TrimPrefix(action.Pipe.String(), ".")
					elems.vars[cleanKey] = ""
				}
			}
		}

		tmpls = append(tmpls, elems)
	}

	return tmpls, nil
}

func (s *Service) Save(filename, hostname string, _ bool, source io.Reader) error {
	sfCli, filename, _, err := s.LoadFs(filename, hostname)
	if err != nil {
		return fmt.Errorf("resolve destination: %w", err)
	}

	// pkg/sftp recommends WRITE|CREATE|TRUNC for write-only compatibility.
	// Several SFTP servers reject WRITE|TRUNC with "permission denied", even
	// when the destination already exists and is writable. O_CREATE does not
	// replace an existing file; it only makes the open request portable and
	// recreates a file that disappeared between loading and saving.
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC

	dest, err := sfCli.OpenFile(filename, flag, os.ModePerm)
	if err != nil {
		return fmt.Errorf("open destination: %w", err)
	}

	_, copyErr := io.Copy(dest, source)
	closeErr := dest.Close()
	if copyErr != nil {
		return fmt.Errorf("write destination: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close destination: %w", closeErr)
	}

	return nil
}

func (s *Service) getFileContents(filename, hostname string) ([]byte, error) {
	fsCli, fullpath, _, err := s.LoadFs(filename, hostname)
	if err != nil {
		return nil, err
	}
	file, err := fsCli.ReadFile(fullpath)
	if err != nil {
		return nil, err
	}

	return file, err
}

func (s *Service) LoadFilePath(filename, hostname string, download bool) (io.ReadSeekCloser, time.Time, error) {
	cliFs, relpath, _, err := s.LoadFs(filename, hostname)
	if err != nil {
		return nil, time.Time{}, err
	}

	if download {
		stat, err := cliFs.Stat(relpath)
		if err != nil {
			return nil, time.Time{}, err
		}

		if stat.IsDir() {
			// convert to zip
			// todo
		} else {
			return cliFs.LoadFile(relpath)
		}
	}

	file, t, err := cliFs.LoadFile(relpath)
	if err != nil {
		return nil, time.Time{}, err
	}

	err = CheckFileType(file)
	if err != nil {
		// file cannot be opened close it before return the err
		fileutil.Close(file)
		return nil, time.Time{}, errors.Join(ErrFileNotSupported, err)
	}

	// reset seek pointer after checking trghe file mime
	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("failed to seek: %w", err)
	}

	return file, t, err
}

var ErrFileNotSupported = errors.New("file type not supported")

func CheckFileType(reader io.Reader) error {
	mtype, err := mimetype.DetectReader(reader)
	if err != nil {
		return err
	}

	// Almost all text formats (json, yaml, txt)
	// inherit from "text/plain"
	// todo
	isText := false
	for m := mtype; m != nil; m = m.Parent() {
		if m.Is("text/plain") {
			return nil
		}
	}

	// Explicit check for SQLite
	if mtype.String() == "application/x-sqlite3" {
		return fmt.Errorf("SQLite not allowed")
	}

	if !isText {
		return fmt.Errorf("binary files not allowed")
	}

	return nil
}

func (s *Service) LoadAll(filename string, hostname string) (
	fs filesystem.FileSystem,
	relpath string,
	alias string,
	err error,
) {
	filename, pathAlias, err := utils.ExtractMeta(filename)
	if err != nil {
		return nil, "", "", err
	}

	fsCli, err := s.Fs(hostname, pathAlias)
	if err != nil {
		return nil, "", "", err
	}

	return fsCli, filename, pathAlias, nil
}

// LoadFs FS gets the correct client -> alias -> path
func (s *Service) LoadFs(filename string, hostname string) (fs filesystem.FileSystem, relpath string, alias string, err error) {
	fsCli, relpath, alias, err := s.LoadAll(filename, hostname)
	if err != nil {
		return nil, "", alias, err
	}
	return fsCli, relpath, alias, nil
}

func (s *Service) Format(filename string, hostname string) ([]byte, error) {
	sfCLi, filename, _, err := s.LoadFs(filename, hostname)
	if err != nil {
		return nil, err
	}

	contents, err := sfCLi.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("unable to read file %w", err)
	}

	ext := filepath.Ext(filename)
	formatter, ok := availableFormatters[ext]
	if ok {
		return formatter(contents)
	}

	return contents, nil
}

type SearchResult struct {
	Value   string
	Indexes []int
}

func (s *Service) search(hostname string, query string, allPaths []string) []SearchResult {
	limit := s.dockYml(hostname).SearchLimit

	matches := fuzzy.Find(query, allPaths)

	if len(matches) < limit {
		limit = len(matches)
	}
	results := make([]SearchResult, limit)
	for i := 0; i < limit; i++ {
		results[i] = SearchResult{
			Value:   matches[i].Str,
			Indexes: matches[i].MatchedIndexes,
		}
	}

	return results
}

func (s *Service) listAllForSearch(dirPath string, hostname string) ([]string, error) {
	fsCli, rel, _, err := s.LoadAll(dirPath, hostname)
	if err != nil {
		return nil, err
	}

	root := fsCli.Root()

	var filez []string
	err = fsCli.WalkDir(rel, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// fs.WalkDir hands the callback a nil entry when the ROOT itself
			// cannot be stat-ed - the folder was deleted or renamed while the
			// search ran - and d.IsDir() on it was a nil dereference that took
			// the whole request down. That case is a real error and is
			// reported; a subdirectory that merely cannot be read is skipped so
			// one unreadable folder does not silence the entire search.
			if d == nil {
				return walkErr
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		left := strings.TrimPrefix(path, root)
		left = strings.TrimPrefix(left, string(filepath.Separator))
		if left != "" {
			filez = append(filez, left)
		}

		return nil
	})

	return filez, err
}

func (s *Service) sortFiles(a, b *Entry, host string) int {
	ra := s.getSortRank(a, host)
	rb := s.getSortRank(b, host)

	if ra < rb {
		return -1
	}
	if ra > rb {
		return 1
	}

	// Same rank: compare names case-insensitively (VS Code style), falling back
	// to a case-sensitive compare so entries differing only by case stay in a
	// deterministic order. A dotfile's leading "." naturally floats it to the
	// top of its group.
	an, bn := filepath.Base(a.fullpath), filepath.Base(b.fullpath)
	if c := strings.Compare(strings.ToLower(an), strings.ToLower(bn)); c != 0 {
		return c
	}
	return strings.Compare(an, bn)
}

// IsPinned reports whether an entry's basename is pinned in the host's
// dockman.yml (pinnedFiles). Exposed so the RPC layer can flag pinned entries
// for the UI without duplicating the pin lookup.
func (s *Service) IsPinned(host, fullpath string) bool {
	_, ok := s.dockYml(host).PinnedFiles[filepath.Base(fullpath)]
	return ok
}

// getSortRank orders entries folders-first, then files: pinned files win (in
// their configured order), then directories, then files (with a small bias so
// compose/yaml files surface first). Dotfiles are NOT a separate group — the
// case-insensitive name compare in sortFiles floats them to the top of their
// own group, matching the folders-first behaviour of editors like VS Code.
func (s *Service) getSortRank(entry *Entry, host string) int {
	conf := s.dockYml(host)

	base := filepath.Base(entry.fullpath)
	// Pinned files (explicit user-defined order) always come first.
	if priority, ok := conf.PinnedFiles[base]; ok {
		// potential bug, but if someone is manually writing the order of 100000 files i say get a life
		// -999 > -12 in this context, pretty stupid but i cant be bothered to fix this mathematically
		return priority - 100_000
	}

	// Directories before files.
	if entry.isDir {
		return 0
	}

	// Files, ranked so compose/yaml files surface first within the group.
	return 1 + s.getFileSortRank(entry.fullpath)
}

// getFileSortRank assigns priority within normal files
func (s *Service) getFileSortRank(filename string) int {
	base := filepath.Base(filename)
	// Priority 0: docker-compose files
	if strings.HasSuffix(base, "compose.yaml") || strings.HasSuffix(base, "compose.yml") {
		return 0
	}
	// Priority 1: other yaml/yml
	if strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml") {
		return 1
	}
	// Priority 2: everything else
	return 2
}
