package gitsync

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

func reading(contents string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(contents)), nil }
}

// The interface offers an "All files" profile and tells the operator that
// "secrets ... remain protected", and the transfer dialog promises to hold back
// "tokens, private keys, or .env secrets". The name list behind those promises
// enumerated id_rsa and id_ed25519 and stopped there, so an ECDSA or DSA key
// sitting next to a compose file was collected as ordinary text and pushed to
// the remote with no opt-in and no typed confirmation.
func TestEverySSHPrivateKeyNameIsHeldBack(t *testing.T) {
	for _, name := range []string{
		"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
		"id_ecdsa_sk", "id_ed25519_sk", "id_rsa.old",
	} {
		require.True(t, isSensitivePath("stack/"+name), "%s must require the sensitive opt-in", name)
	}
}

// Names are a weak signal - the operator chooses them. Private key material
// announces itself in its first line whatever the file is called, which is the
// lesson isAgeIdentity already applies to age identities.
func TestPrivateKeyMaterialIsRecognisedByContent(t *testing.T) {
	for _, header := range []string{
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNza\n",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIB\n",
		"-----BEGIN EC PRIVATE KEY-----\nMHcCAQEE\n",
		"-----BEGIN PRIVATE KEY-----\nMIIEvQIB\n",
		"-----BEGIN ENCRYPTED PRIVATE KEY-----\nMIIFHDBO\n",
		"-----BEGIN PGP PRIVATE KEY BLOCK-----\nlQdGBGX\n",
		"PuTTY-User-Key-File-3: ssh-rsa\nEncryption: none\n",
	} {
		body := header
		require.True(t, isPrivateKeyMaterial(int64(len(body)), reading(body)),
			"unrecognised key material: %.40q", header)
	}
}

func TestPrivateKeyDetectionLeavesOrdinaryFilesAlone(t *testing.T) {
	require.False(t, isPrivateKeyMaterial(0, reading("")))
	public := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI user@host\n"
	require.False(t, isPrivateKeyMaterial(int64(len(public)), reading(public)),
		"a public key is not key material to hold back")
	certificate := "-----BEGIN CERTIFICATE-----\nMIIDdTCCAl2gA\n"
	require.False(t, isPrivateKeyMaterial(int64(len(certificate)), reading(certificate)),
		"a certificate carries no private half")
	compose := "services:\n  web:\n    image: nginx\n"
	require.False(t, isPrivateKeyMaterial(int64(len(compose)), reading(compose)))
	// The marker has to be near the start; a document merely quoting it in
	// prose, far in, is not a key file.
	require.False(t, isPrivateKeyMaterial(maxKeyMaterialScan+1, reading("-----BEGIN RSA PRIVATE KEY-----")),
		"the scan is bounded, an oversized file is not read")
}

// Credential files that no rule named: direnv exports, curl and Postgres
// credentials, registry and cluster configurations, Terraform state.
func TestConventionalCredentialFilesAreHeldBack(t *testing.T) {
	for _, name := range []string{
		".envrc", ".netrc", "_netrc", ".pgpass", ".npmrc", ".htpasswd",
		".dockercfg", "kubeconfig", "terraform.tfstate", "terraform.tfstate.backup",
		"vault.jks", "release.keystore", "server.ppk", "backup.asc", "backup.gpg",
	} {
		require.True(t, isSensitivePath("stack/"+name), "%s must require the sensitive opt-in", name)
	}
}

// "token" and "password" are as telling as "secret" and "credential", which
// were already covered.
func TestTokenAndPasswordNamesAreHeldBack(t *testing.T) {
	for _, name := range []string{
		"api_token", "token.txt", "auth-token.json", "password.txt",
		"passwords.yml", "db.passwd", "APIKEY", "api_key.txt",
	} {
		require.True(t, isSensitivePath("stack/"+name), "%s must require the sensitive opt-in", name)
	}
}

// And they must stay word-shaped, or ordinary application sources start
// demanding a confirmation nobody should have to give.
func TestOrdinaryFilesStillTransferWithoutConfirmation(t *testing.T) {
	for _, name := range []string{
		"compose.yml", "docker-compose.yaml", "tokenizer.py", "tokens.md.tmpl",
		"README.md", "nginx.conf", "Caddyfile", "passwordless-setup.md",
		".env.example", ".env.sample", ".env.template", ".env.dist",
		"index.html", "app.js", "keystore-migration.sql",
	} {
		require.False(t, isSensitivePath("stack/"+name), "%s must not require the sensitive opt-in", name)
	}
}

// The end-to-end shape: an ECDSA key next to a compose file is reported and
// held back rather than collected for transfer.
func TestAnECDSAKeyIsNotCollectedForTransfer(t *testing.T) {
	root := t.TempDir()
	stack := filepath.Join(root, "app")
	require.NoError(t, os.MkdirAll(stack, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stack, "compose.yml"), []byte("services: {}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(stack, "id_ecdsa"),
		[]byte("-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEIB\n-----END EC PRIVATE KEY-----\n"), 0o600))
	// Named so no rule can recognise it, but it announces itself in its body.
	require.NoError(t, os.WriteFile(filepath.Join(stack, "deploy.pub.backup"),
		[]byte("-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1r\n"), 0o600))

	files, err := collectStackFiles(filesystem.NewLocal(root), "app", false)
	require.NoError(t, err)

	for _, name := range []string{"id_ecdsa", "deploy.pub.backup"} {
		entry, listed := files[name]
		require.True(t, listed, "%s must be reported, not silently dropped", name)
		require.Equal(t, "sensitive", entry.skipReason, "%s was collected for transfer", name)
		require.Nil(t, entry.open, "%s must carry no reader", name)
	}

	compose, ok := files["compose.yml"]
	require.True(t, ok)
	require.Empty(t, compose.skipReason, "ordinary stack files are unaffected")
}
