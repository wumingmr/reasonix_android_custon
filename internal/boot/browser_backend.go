package boot

import (
	"time"

	"reasonix/internal/browser"
	"reasonix/internal/browser/cdp"
	"reasonix/internal/config"
)

// browserLaunchTimeout bounds the first attach: a browser that has not shown a
// DevTools endpoint by then is reported to the model instead of hanging a turn.
const browserLaunchTimeout = 45 * time.Second

// browserBackend decides which browser a session drives, and returns the
// shutdown the controller owes it. A host that already brought one — the
// desktop shell's WebContentsViews or Serve's SSH broker — keeps it and stays
// responsible for its lifetime. Otherwise a configured CDP backend gives CLI,
// Serve, and headless sessions the same browser_* tools; nothing is launched
// until the first browser tool call.
// uploadRoots are the session's write roots: a page is untrusted, so a file
// input may only receive files this task already owns.
func browserBackend(host browser.Executor, cfg config.BrowserConfig, uploadRoots []string) (browser.Executor, func()) {
	if host != nil {
		return host, nil
	}
	if !cfg.Enabled {
		return nil, nil
	}
	lazy := cdp.NewLazy(cdp.Options{
		Endpoint:            cfg.Endpoint,
		AllowRemoteEndpoint: cfg.AllowRemoteEndpoint,
		ChromePath:          cfg.ChromePath,
		ChromeArgs:          cfg.ChromeArgs,
		UserDataDir:         cfg.UserDataDir,
		Headless:            cfg.Headless,
		LaunchTimeout:       browserLaunchTimeout,
		UploadRoots:         uploadRoots,
	})
	return lazy, lazy.Shutdown
}
