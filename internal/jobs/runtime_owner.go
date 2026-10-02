package jobs

// Recorder ownership is immutable once the manager starts admitting jobs.
func (m *Manager) TaskRuntimeOwnerID() string {
	if owner, ok := m.boundRecorder().(interface{ RuntimeOwnerID() string }); ok {
		return owner.RuntimeOwnerID()
	}
	return ""
}
