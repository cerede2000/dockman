package files

import (
	b64 "encoding/base64"
	"testing"
)

func TestDecodeUploadFilename(t *testing.T) {
	t.Parallel()

	paths := []string{
		"compose/docker-compose.yml",
		"production/configuration avec espaces.yaml",
		"données/élément-€-東京.txt",
	}

	for _, path := range paths {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			encoded := b64.RawURLEncoding.EncodeToString([]byte(path))
			decoded, err := decodeUploadFilename(encoded)
			if err != nil {
				t.Fatalf("decode URL-safe filename: %v", err)
			}
			if string(decoded) != path {
				t.Fatalf("decoded path %q, want %q", decoded, path)
			}
		})
	}
}

func TestDecodeUploadFilenameAcceptsLegacyBase64(t *testing.T) {
	t.Parallel()

	const path = "compose/legacy.yml"
	encoded := b64.StdEncoding.EncodeToString([]byte(path))
	decoded, err := decodeUploadFilename(encoded)
	if err != nil {
		t.Fatalf("decode legacy filename: %v", err)
	}
	if string(decoded) != path {
		t.Fatalf("decoded path %q, want %q", decoded, path)
	}
}

// The editor's only protection against overwriting a change that landed while a
// file was open is the revision it loaded and sends back. Without it the save
// used to go through with no check at all, silently, which is exactly what a
// failed revision computation produced.
func TestAnEditorSaveWithoutARevisionIsRefused(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		session  string
		revision string
		create   bool
		refused  bool
	}{
		{name: "editor save carrying its revision", session: "s-1", revision: `"abc"`, refused: false},
		{name: "editor save with no revision", session: "s-1", revision: "", refused: true},
		{name: "editor save with a blank revision", session: "s-1", revision: `  ""  `, refused: true},
		{name: "editor creating a new file", session: "s-1", revision: "", create: true, refused: false},
		{name: "drag and drop upload", session: "", revision: "", refused: false},
		{name: "upload of a large file", session: "   ", revision: "", refused: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := editorSaveNeedsRevision(testCase.session, testCase.revision, testCase.create)
			if got != testCase.refused {
				t.Fatalf("editorSaveNeedsRevision = %v, want %v", got, testCase.refused)
			}
		})
	}
}
