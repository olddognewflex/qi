//go:build !unix

package vault

// withFileLock provides no cross-process locking on non-unix platforms; it
// simply runs fn. The per-write content guard and atomic rename still apply,
// so single-process correctness holds — only the cross-process race remains
// uncovered here. lockPathFor is unused on these platforms.
func withFileLock(_ string, fn func() error) error {
	return fn()
}
