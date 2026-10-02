package boot

import (
	"io"
	"os"
	"strings"

	"reasonix/internal/skill"
	"reasonix/internal/skill/skillwatch"
)

// watchSkillsEnabled reports whether production controllers own physical
// watchers. Package fixtures opt out so they cannot exhaust descriptors, while
// store watcher tests opt in explicitly.
func watchSkillsEnabled() bool {
	return !strings.HasSuffix(strings.TrimSuffix(os.Args[0], ".exe"), ".test")
}

func newSkillWatchService(enabled bool, stderr io.Writer) *skillwatch.Service {
	if !enabled {
		return nil
	}
	return skillwatch.NewService(skillwatch.Options{Stderr: stderr})
}

// buildSkillWatchService picks the one watch service a build's enabled and
// all-skill stores share: the caller's host-lifetime service when supplied
// (hostOwned), otherwise one this build owns and closes.
func buildSkillWatchService(shared *skillwatch.Service, enabled bool, stderr io.Writer) (svc *skillwatch.Service, hostOwned bool) {
	if shared != nil {
		return shared, true
	}
	return newSkillWatchService(enabled, stderr), false
}

// NewHostSkillWatchService returns a caller-owned watcher that every controller
// on one host can share, or nil when watching is disabled. Pass it as
// Options.SharedSkillWatchService and close it once the host's controllers are
// gone: Build then subscribes each controller to it instead of creating one
// service — and one helper process — per build.
func NewHostSkillWatchService(stderr io.Writer) *skillwatch.Service {
	return newSkillWatchService(watchSkillsEnabled(), stderr)
}

// closeSkillsWithWatcher closes this build's skill stores, and the watch service
// only when this build owns it. A caller-supplied service outlives the build:
// the caller closes it, and closing it here would tear down the helper process
// other controllers on the same host are still watching through.
func closeSkillsWithWatcher(primary, all *skill.Store, watchService **skillwatch.Service, hostOwned bool) {
	closeSkillStores(primary, all)
	if *watchService != nil && !hostOwned {
		_ = (*watchService).Close()
	}
	*watchService = nil
}
