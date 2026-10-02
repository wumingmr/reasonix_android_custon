//go:build windows

package skillwatch

// Windows always routes through the helper process: in-process fsnotify Add
// can block inside ReadDirectoryChangesW while another goroutine closes the
// watcher, which the helper isolates from the host. ForceHelper has nothing to
// switch to on this platform.
func newPlatformBackend(svc *Service, opts Options) (backend, string, *helperClient) {
	start := opts.HelperCommand
	if start == nil {
		start = defaultHelperCommand
	}
	h := newHelperClient(start, svc)
	return h, "helper", h
}
