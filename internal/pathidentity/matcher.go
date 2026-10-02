package pathidentity

import "os"

// Matcher compares paths under one Options, resolving and statting each
// distinct path once. Identity can change between passes (a directory's case
// flag, a relinked junction), so a Matcher must not outlive the pass using it.
type Matcher struct {
	options Options
	resolve func(string, Options) (Identity, error)
	seen    map[string]matchedPath
}

type matchedPath struct {
	identity Identity
	err      error
	info     os.FileInfo
	statErr  error
}

// NewMatcher uses resolve in place of Resolve when it is non-nil.
func NewMatcher(options Options, resolve func(string, Options) (Identity, error)) *Matcher {
	if resolve == nil {
		resolve = Resolve
	}
	return &Matcher{options: options, resolve: resolve, seen: map[string]matchedPath{}}
}

func (m *Matcher) lookup(path string) matchedPath {
	if found, ok := m.seen[path]; ok {
		return found
	}
	var entry matchedPath
	entry.identity, entry.err = m.resolve(path, m.options)
	if entry.err == nil {
		entry.info, entry.statErr = statForMode(entry.identity.AccessPath, m.options.FollowLeaf)
	}
	m.seen[path] = entry
	return entry
}

func (m *Matcher) Same(a, b string) (bool, error) {
	left := m.lookup(a)
	if left.err != nil {
		return false, left.err
	}
	right := m.lookup(b)
	if right.err != nil {
		return false, right.err
	}
	if left.statErr == nil && right.statErr == nil && os.SameFile(left.info, right.info) {
		return true, nil
	}
	for _, statErr := range []error{left.statErr, right.statErr} {
		if statErr != nil && !os.IsNotExist(statErr) {
			return false, classify("verify", "", statErr)
		}
	}
	return left.identity.Key == right.identity.Key, nil
}
