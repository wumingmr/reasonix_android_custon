//go:build darwin || (!windows && !linux && !cgo)

package main

func (a *App) startTrayHealthMonitor(*desktopTray) {}
func (a *App) trayConfigured(*desktopTray)         {}
