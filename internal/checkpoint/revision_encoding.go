package checkpoint

import fileenc "reasonix/internal/fileutil/encoding"

// encodeRevisionContent re-encodes a v1 text capture for restore, in the
// encoding it was captured in or else the one the file holds now.
func (s *Store) encodeRevisionContent(rev FileRevision, abs string) ([]byte, error) {
	enc := fileenc.UTF8
	if rev.Encoding != nil {
		enc = *rev.Encoding
	} else if current := s.detectCurrentEncoding(abs); current != nil {
		enc = *current
	}
	return fileenc.Encode(*rev.Content, enc)
}
