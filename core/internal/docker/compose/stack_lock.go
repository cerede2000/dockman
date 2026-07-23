package compose

import "sync"

type stackActionLock struct {
	mu    sync.Mutex
	users int
}

var stackActionLocks = struct {
	sync.Mutex
	entries map[string]*stackActionLock
}{entries: make(map[string]*stackActionLock)}

// TryLockStack serializes every Dockman Compose action for one host/file,
// including actions started by Git automation and by the regular UI.
func TryLockStack(host, filename string) (func(), bool) {
	key := host + "\x00" + filename
	stackActionLocks.Lock()
	lock := stackActionLocks.entries[key]
	if lock == nil {
		lock = &stackActionLock{}
		stackActionLocks.entries[key] = lock
	}
	lock.users++
	stackActionLocks.Unlock()
	if !lock.mu.TryLock() {
		stackActionLocks.Lock()
		lock.users--
		if lock.users == 0 {
			delete(stackActionLocks.entries, key)
		}
		stackActionLocks.Unlock()
		return nil, false
	}
	return func() {
		lock.mu.Unlock()
		stackActionLocks.Lock()
		lock.users--
		if lock.users == 0 {
			delete(stackActionLocks.entries, key)
		}
		stackActionLocks.Unlock()
	}, true
}
