package config

import "reflect"

// ProviderEntryConfigSnapshot strips process-only state from a provider copy so
// optimistic edit logs contain only persisted configuration.
func ProviderEntryConfigSnapshot(entry ProviderEntry) ProviderEntry {
	stripRuntimeReasoningDefaults(&entry)
	entry.resolvedAPIKey = ""
	entry.resolvedSource = CredentialSource{}
	entry.visionOverride = nil
	entry.persistedOfficialCurrency = ""
	return entry
}

// ProviderEntriesConfigEqual compares persisted provider configuration while
// ignoring credentials and capability state resolved only for the current
// process. Setup uses it for optimistic conflict detection during replay.
func ProviderEntriesConfigEqual(a, b ProviderEntry) bool {
	return reflect.DeepEqual(ProviderEntryConfigSnapshot(a), ProviderEntryConfigSnapshot(b))
}
