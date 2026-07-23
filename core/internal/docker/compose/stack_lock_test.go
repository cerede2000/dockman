package compose

import "testing"

func TestStackActionLockIsScopedByHostAndFile(t *testing.T) {
	unlock, ok := TryLockStack("local", "compose/app/compose.yml")
	if !ok {
		t.Fatal("first lock refused")
	}
	if _, ok := TryLockStack("local", "compose/app/compose.yml"); ok {
		t.Fatal("duplicate lock accepted")
	}
	otherUnlock, ok := TryLockStack("remote", "compose/app/compose.yml")
	if !ok {
		t.Fatal("different host was blocked")
	}
	otherUnlock()
	unlock()
	retryUnlock, ok := TryLockStack("local", "compose/app/compose.yml")
	if !ok {
		t.Fatal("released lock could not be reacquired")
	}
	retryUnlock()
}
