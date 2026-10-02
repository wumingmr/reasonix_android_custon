package checkpoint

// Option overrides a Store default at construction time.
type Option func(*Store)

// WithRetainCheckpoints caps how many turns of file payloads the store retains.
// A value below 1 is ignored, leaving DefaultRetainCheckpoints in place.
func WithRetainCheckpoints(turns int) Option {
	return func(s *Store) {
		if turns > 0 {
			s.retainN = turns
		}
	}
}

// WithBlobQuota sets the soft byte budget for retained file payloads. A value
// below 1 is ignored, leaving DefaultBlobQuotaBytes in place.
func WithBlobQuota(bytes int64) Option {
	return func(s *Store) {
		if bytes > 0 {
			s.blobQuota = bytes
		}
	}
}
