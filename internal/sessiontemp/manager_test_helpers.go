package sessiontemp

import filelock "reasonix/internal/identitylock"

func tryLockForTest(path string) (func(), error) {
	return filelock.Acquire(nilContext(), path)
}
