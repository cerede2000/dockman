package main

import "testing"

func TestCleanRootUsesDot(t *testing.T) {
	if actual := clean("/"); actual != "." {
		t.Fatalf("clean(/) = %q; want .", actual)
	}
}
