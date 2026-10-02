package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"reasonix/internal/config"
	"reasonix/internal/i18n"
)

func (s *providerSetupSession) addPrompted(in *bufio.Scanner, w io.Writer, result providerPromptResult) bool {
	if result.keyEnvTyped && len(result.credentials) > 0 && len(result.entries) > 0 {
		names := make([]string, 0, len(result.entries))
		for _, entry := range result.entries {
			names = append(names, entry.Name)
		}
		from := result.entries[0].APIKeyEnv
		to, typed := s.settleTypedKeyEnv(in, w, keyEnvPromptLabel(result.entries[0].Kind), names, from, result.keyEnvDraft)
		result.renameKeyEnv(from, to)
		result.keyEnvTyped = typed
	}
	for _, entry := range result.entries {
		if !confirmSharedCredential(s.cfg, entry, "") {
			return false
		}
	}
	if err := s.add(result.entries); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false
	}
	s.addProviderAccess(result.entries)
	for key, value := range result.credentials {
		var names []string
		for _, entry := range result.entries {
			if entry.APIKeyEnv == key {
				names = append(names, entry.Name)
			}
		}
		if err := s.setCredentialForProviders(names, key, value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return false
		}
		if result.keyEnvTyped {
			for _, name := range names {
				s.typedKeyEnv[name] = key
			}
		}
	}
	// After the new keys are staged, so usability sees them.
	s.promoteDefaultToNewProviders(result.entries)
	return true
}

func (r *providerPromptResult) renameKeyEnv(from, to string) {
	if from == to {
		return
	}
	for i := range r.entries {
		if r.entries[i].APIKeyEnv == from {
			r.entries[i].APIKeyEnv = to
		}
	}
	if value, ok := r.credentials[from]; ok {
		delete(r.credentials, from)
		r.credentials[to] = value
	}
}

// settleTypedKeyEnv asks again while providers may not claim key, saying who
// holds it. Pressing Enter returns draft with typed=false: a private slot.
func (s *providerSetupSession) settleTypedKeyEnv(in *bufio.Scanner, w io.Writer, label string, providers []string, key, draft string) (string, bool) {
	for {
		var held error
		for _, name := range providers {
			if held = s.cfg.CredentialKeyClaimable(key, name); held != nil {
				break
			}
		}
		if held == nil {
			return key, true
		}
		fmt.Fprintln(w, keyEnvInUseText(held))
		fmt.Fprintln(w, i18n.M.SetupKeyEnvRetry)
		var typed bool
		if key, typed = promptAPIKeyEnvName(in, w, label, draft); !typed {
			return key, false
		}
	}
}

func keyEnvPromptLabel(kind string) string {
	if kind == "anthropic" {
		return i18n.M.AnthropicPromptKeyEnv
	}
	return i18n.M.CustomPromptKeyEnv
}

func keyEnvInUseText(err error) string {
	var inUse *config.CredentialKeyInUseError
	if !errors.As(err, &inUse) {
		return err.Error()
	}
	switch inUse.Holder {
	case config.CredentialKeyHeldByProvider:
		return fmt.Sprintf(i18n.M.SetupKeyEnvTakenFmt, inUse.Key, inUse.Provider)
	case config.CredentialKeyHeldBySetting:
		return fmt.Sprintf(i18n.M.SetupKeyEnvSettingFmt, inUse.Key)
	case config.CredentialKeyHeldByEnvironment:
		return fmt.Sprintf(i18n.M.SetupKeyEnvShellFmt, inUse.Key)
	default:
		return fmt.Sprintf(i18n.M.SetupKeyEnvStoredFmt, inUse.Key)
	}
}
