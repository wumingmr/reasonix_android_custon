package bot

import (
	"context"

	"reasonix/internal/session"
)

// finishSessionTeardown retires the last sessions and then releases the
// services. CloseAll refuses a runtime that is still bound, so the order
// matters.
func (gw *BotGateway) finishSessionTeardown() {
	gw.closeSessions()
	gw.closeSessionServices()
}

// Session services are cached per root and outlive the individual sessions, so
// they still own the writer lease after the last state is retired. A held lease
// keeps the directory unremovable on Windows and unopenable by the next process.
func (gw *BotGateway) closeSessionServices() {
	gw.sessionServicesMu.Lock()
	services := make([]*session.Service, 0, len(gw.sessionServices))
	for root, service := range gw.sessionServices {
		services = append(services, service)
		delete(gw.sessionServices, root)
	}
	gw.sessionServicesMu.Unlock()
	for _, service := range services {
		if err := service.CloseAll(context.Background()); err != nil {
			gw.logger.Warn("error releasing bot session writer lease", "err", err)
		}
	}
}
