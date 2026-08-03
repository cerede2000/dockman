package updater

import "testing"

func TestRepositoryDigests(t *testing.T) {
	t.Parallel()
	digests := []string{
		"docker.io/library/alpine@sha256:aaa",
		"ghcr.io/example/app@sha256:bbb",
		"mirror.example/app@sha256:aaa",
		"invalid",
	}
	want := []string{"aaa", "bbb"}
	got := repositoryDigests(digests)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("repositoryDigests() = %v, want %v", got, want)
	}
}
