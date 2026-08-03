package updater

import "testing"

func TestRepositoryDigestsForImage(t *testing.T) {
	t.Parallel()
	digests := []string{
		"alpine@sha256:aaa",
		"ghcr.io/example/app@sha256:bbb",
	}
	tests := []struct {
		name  string
		image string
		want  []string
	}{
		{name: "docker hub short name", image: "alpine:3.22", want: []string{"aaa"}},
		{name: "explicit public registry", image: "ghcr.io/example/app:latest", want: []string{"bbb"}},
		{name: "local retag of pulled image", image: "apple-music-rip:local", want: nil},
		{name: "local image without digest", image: "demo:local", want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := repositoryDigestsForImage(digests, test.image)
			if len(got) != len(test.want) {
				t.Fatalf("repositoryDigestsForImage() = %v, want %v", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("repositoryDigestsForImage() = %v, want %v", got, test.want)
				}
			}
		})
	}
}
