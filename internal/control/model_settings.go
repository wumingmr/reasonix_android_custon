package control

import (
	"crypto/rand"
	"errors"
	"fmt"
)

var ErrModelChoiceStale = errors.New("model configuration or runtime changed; review the current settings")
var ErrModelContinuationDenied = errors.New("current configuration cannot be used")

type ModelApplicationChoice struct {
	Mode                    string `json:"mode"`
	ExpectedAppliedRevision string `json:"expectedAppliedRevision"`
	ExpectedDesiredRevision string `json:"expectedDesiredRevision"`
	ExpectedRuntimeIdentity string `json:"expectedRuntimeIdentity"`
	ConfirmationToken       string `json:"confirmationToken,omitempty"`
}

func (c *Controller) ModelRuntimeIdentity() string  { return c.modelSettings.identity }
func (c *Controller) ModelConnectionTarget() string { return c.modelSettings.connectionTarget }

func (c *Controller) ValidateModelContinuation() error {
	if c.modelSettings.continuation == nil {
		return fmt.Errorf("runtime cannot verify the current configuration")
	}
	return c.modelSettings.continuation()
}

func (c *Controller) ValidateModelApplicationChoice(choice ModelApplicationChoice) error {
	if choice.Mode != "applied_once" {
		return fmt.Errorf("unsupported model application choice")
	}
	applied, desired, err := c.ModelSettingsState()
	if err != nil {
		return err
	}
	if choice.ExpectedRuntimeIdentity != c.ModelRuntimeIdentity() || choice.ExpectedAppliedRevision != applied || choice.ExpectedDesiredRevision != desired {
		return ErrModelChoiceStale
	}
	if err := c.ValidateModelContinuation(); err != nil {
		return errors.Join(ErrModelContinuationDenied, err)
	}
	_, latest, err := c.ModelSettingsState()
	if err != nil {
		return err
	}
	if latest != desired {
		return ErrModelChoiceStale
	}
	return nil
}

// TracksModelSettings reports whether the host supplied a live configuration
// observer. Unmanaged controllers have no settings I/O to perform on admission.
func (c *Controller) TracksModelSettings() bool { return c.modelSettings.current != nil }

// ModelSettingsState compares this immutable runtime with current disk config.
// It is intentionally separate from provider-visible messages and metadata.
func (c *Controller) ModelSettingsState() (applied, desired string, err error) {
	if c.modelSettings.current == nil {
		return "", "", nil
	}
	desired, err = c.modelSettings.current()
	return c.modelSettings.revision, desired, err
}

// ModelSettingsSourceRevision identifies an immutable Desktop resolver bundle.
// It is transport bookkeeping only, never part of the conversation.
func (c *Controller) ModelSettingsSourceRevision() string { return c.modelSettings.sourceRevision }

// controllerModelSettings keeps the immutable snapshot and its host admission
// callback together for the lifetime of one controller. The callback is guarded
// by Controller.mu; snapshot fields are immutable after construction.
type controllerModelSettings struct {
	connectionTarget    string
	identity            string
	continuation        func() error
	revision            string
	sourceRevision      string
	current             func() (string, error)
	beforeInboxDispatch func(*Controller) (func(), error)
}

func newControllerModelSettings(opts Options) controllerModelSettings {
	return controllerModelSettings{
		connectionTarget:    opts.ModelConnectionTarget,
		identity:            rand.Text(),
		continuation:        opts.ModelSettingsContinuation,
		revision:            opts.ModelSettingsRevision,
		sourceRevision:      opts.ModelSettingsSourceRevision,
		current:             opts.ModelSettingsCurrent,
		beforeInboxDispatch: opts.BeforeInboxDispatch,
	}
}
