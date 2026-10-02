package control

// ValidateModelApplicationIdentity fences recovery actions without requiring
// permission to send using the old credentials. Stopping work remains possible
// after provider access has been revoked.
func (c *Controller) ValidateModelApplicationIdentity(choice ModelApplicationChoice) error {
	applied, desired, err := c.ModelSettingsState()
	if err != nil {
		return err
	}
	if choice.ExpectedRuntimeIdentity != c.ModelRuntimeIdentity() || choice.ExpectedAppliedRevision != applied || choice.ExpectedDesiredRevision != desired {
		return ErrModelChoiceStale
	}
	return nil
}

// CancelModelApplicationBlockers never cancels independent session processes.
// The caller holds the host's session admission lock through validation/cancel.
func (c *Controller) CancelModelApplicationBlockers(ids []string) {
	allowed := map[string]bool{}
	for _, job := range c.ModelReplacementJobs() {
		allowed[job.ID] = true
	}
	for _, id := range ids {
		if allowed[id] {
			c.CancelJob(id)
		}
	}
}
