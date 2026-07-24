package tui

// This file defines the typed messages that flow through the Bubble Tea
// model in app.go. The read-only foundation (issue #338) only needs a
// message to (re)load the derived dashboard view-model; async action and
// per-store probe messages arrive with issue #339.

// DashboardLoadedMsg carries a freshly derived dashboard view-model into the
// model. It is emitted by the command returned from ReloadDashboardCmd.
type DashboardLoadedMsg struct {
	Dashboard Dashboard
}

// DashboardLoadError reports that a dashboard (re)load failed. The model keeps
// showing the last good dashboard and surfaces the error in the footer.
type DashboardLoadError struct {
	Err error
}
