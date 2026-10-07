package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/boot"
	"reasonix/internal/config"
)

type connectionSetup struct {
	providerName string
	configPath   string
	revision     string
	key          string
	saving       bool
	testing      bool
	testCancel   context.CancelFunc
	testVersion  uint64
}

type connectionCredentialTestedMsg struct {
	providerName string
	setup        *connectionSetup
	version      uint64
	err          error
}

type connectionCredentialSavedMsg struct {
	providerName string
	result       config.ConnectionCredentialResult
	err          error
}

func (m *chatTUI) openConnectionSetup() {
	cfg, err := config.LoadForRootReadOnly(m.ctrl.WorkspaceRoot())
	if err != nil {
		m.notice("setup: " + err.Error())
		return
	}
	current := strings.SplitN(m.modelRef, "/", 2)[0]
	items := make([]quickPickerItem, 0, len(cfg.Providers))
	selected := 0
	for _, entry := range cfg.Providers {
		if strings.TrimSpace(entry.Name) == "" || len(entry.ModelList()) == 0 {
			continue
		}
		status := ""
		if entry.Name == current {
			status = "active"
			selected = len(items)
		}
		description := fmt.Sprintf("%s · %d model(s)", entry.Kind, len(entry.ModelList()))
		if entry.RequiresAPIKey() && entry.APIKey() == "" {
			description += " · key required"
		}
		items = append(items, quickPickerItem{ID: entry.Name, Label: entry.Name, Description: description, Status: status})
	}
	if len(items) == 0 {
		m.notice("setup: no model connections are available")
		return
	}
	m.quickPick = &quickPicker{kind: quickPickerSetupProvider, title: "Configure connection", items: items, selected: selected}
}

func (m *chatTUI) beginConnectionKeyEdit(providerName string) {
	root := strings.TrimSpace(m.ctrl.WorkspaceRoot())
	// Resolve the same source precedence as runtime loading: a user provider
	// shadows a same-named project declaration.
	effective, err := config.LoadForRootReadOnly(root)
	if err != nil {
		m.notice("setup: " + err.Error())
		return
	}
	path, err := effective.ProviderEditPath(root, providerName)
	if err != nil {
		m.notice("setup: " + err.Error())
		return
	}
	m.setup = &connectionSetup{providerName: providerName, configPath: path, revision: config.ConfigFileRevision(path)}
}

func (m chatTUI) handleConnectionSetupKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	setup := m.setup
	if setup == nil || setup.saving {
		return m, nil
	}
	switch msg.String() {
	case "esc":
		if setup.testCancel != nil {
			setup.testCancel()
		}
		m.setup = nil
		return m, nil
	case "ctrl+t":
		if setup.testing {
			return m, nil
		}
		if setup.key == "" {
			m.notice("setup: enter an API key before testing")
			return m, nil
		}
		cfg, err := config.LoadForEditReadOnlyStrict(setup.configPath)
		if err != nil {
			m.notice("setup: " + err.Error())
			return m, nil
		}
		entry, ok := cfg.Provider(setup.providerName)
		if !ok {
			m.notice("setup: connection no longer exists")
			return m, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		setup.testVersion++
		version := setup.testVersion
		setup.testing, setup.testCancel = true, cancel
		probeEntry := *entry
		key := setup.key
		proxy := cfg.NetworkProxySpec()
		return m, func() tea.Msg {
			err := boot.ProbeProviderConnection(ctx, probeEntry, key, proxy)
			return connectionCredentialTestedMsg{providerName: setup.providerName, setup: setup, version: version, err: err}
		}
	case "backspace":
		if setup.key != "" {
			setup.invalidateTest()
			_, n := utf8.DecodeLastRuneInString(setup.key)
			setup.key = setup.key[:len(setup.key)-n]
		}
		return m, nil
	case "enter":
		if setup.key == "" {
			m.notice("setup: enter an API key")
			return m, nil
		}
		setup.invalidateTest()
		requestID, err := newConnectionSetupRequestID()
		if err != nil {
			m.notice("setup: " + err.Error())
			return m, nil
		}
		setup.saving = true
		req := config.ConnectionCredentialRequest{
			RequestID: requestID, ConfigPath: setup.configPath,
			ProviderNames: []string{setup.providerName}, Key: setup.key, ExpectedRevision: setup.revision,
		}
		return m, func() tea.Msg {
			result, err := config.CommitConnectionCredential(req)
			return connectionCredentialSavedMsg{providerName: setup.providerName, result: result, err: err}
		}
	default:
		text := msg.Text
		if text == "" {
			s := msg.String()
			if len(s) == 1 && s[0] >= 32 && s[0] < 127 {
				text = s
			}
		}
		if text != "" && !strings.ContainsAny(text, "\r\n") {
			setup.invalidateTest()
			setup.key += text
		}
		return m, nil
	}
}

func (m chatTUI) renderConnectionSetup() string {
	if m.setup == nil {
		return ""
	}
	w := max(m.width, 10)
	contentWidth := max(w-8, 12)
	var body strings.Builder
	body.WriteString(accent("Configure "+m.setup.providerName) + "\n")
	body.WriteString("  API Key\n")
	masked := strings.Repeat("•", utf8.RuneCountInString(m.setup.key))
	if masked == "" {
		masked = dim("Enter credential")
	}
	body.WriteString("  " + ansi.Truncate(masked, contentWidth, "…") + "\n")
	if m.setup.saving {
		body.WriteString(dim("Saving…"))
	} else if m.setup.testing {
		body.WriteString(dim("Testing connection… · Esc cancel"))
	} else {
		body.WriteString(dim("Ctrl+T test · Enter save · Esc cancel"))
	}
	return choicePanelStyle.Width(w).Render(body.String())
}

func (m *chatTUI) handleConnectionCredentialTested(msg connectionCredentialTestedMsg) {
	if m.setup == nil || m.setup != msg.setup || m.setup.providerName != msg.providerName || m.setup.testVersion != msg.version {
		return
	}
	m.setup.testing = false
	m.setup.testCancel = nil
	if msg.err != nil {
		m.notice("connection test: " + msg.err.Error())
		return
	}
	m.notice("Connection test succeeded. The credential has not been saved yet.")
}

func (s *connectionSetup) invalidateTest() {
	if s == nil {
		return
	}
	if s.testCancel != nil {
		s.testCancel()
	}
	s.testing = false
	s.testCancel = nil
	s.testVersion++
}

func newConnectionSetupRequestID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create save request: %w", err)
	}
	return "cli-" + hex.EncodeToString(raw[:]), nil
}

func (m *chatTUI) handleConnectionCredentialSaved(msg connectionCredentialSavedMsg) tea.Cmd {
	if msg.err != nil {
		if m.setup != nil {
			m.setup.saving = false
		}
		m.notice("setup: " + msg.err.Error())
		return nil
	}
	m.setup = nil
	m.notice("Credential saved for " + msg.providerName + ". Applying it to this session…")
	return m.scheduleCurrentControllerRebuild("setup", "Credential saved and applied")
}
