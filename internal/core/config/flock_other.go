//go:build !unix

package config

// lockFile is a no-op outside unix. Windows is not a v0.1 target (see the
// roadmap in README); when it becomes one, this needs LockFileEx, and until
// then the build must not silently pretend the lock is held — hence the
// explicit comment rather than a nil return that reads like success.
func lockFile(_ string) (unlock func(), err error) {
	return func() {}, nil
}
