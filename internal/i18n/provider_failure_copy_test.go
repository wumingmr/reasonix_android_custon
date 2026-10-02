package i18n

import (
	"fmt"
	"strings"
	"testing"
)

func TestProviderFailureCopyDoesNotClaimAutomaticRetry(t *testing.T) {
	for locale, messages := range map[string]Messages{"en": English, "zh": Chinese, "zh-TW": ChineseTraditional} {
		for _, copy := range []string{messages.ProviderErrAuthRejected, messages.ProviderErrRateLimited, messages.ProviderErrServer, messages.ProviderErrServerBusy} {
			for _, retired := range []string{"Retried", "retried", "已退避重试", "已退避重試"} {
				if strings.Contains(copy, retired) {
					t.Errorf("%s claims a request was retried: %s", locale, copy)
				}
			}
		}
		for _, copy := range []string{messages.ProviderErrStreamInterruptedFmt, messages.ProviderErrDisconnectedFmt} {
			if !strings.Contains(fmt.Sprintf(copy, "trace-123"), "trace-123") {
				t.Errorf("%s lost the original diagnostic", locale)
			}
			if locale != "en" && !strings.Contains(copy, "模型") {
				t.Errorf("%s stream failure is not localized: %s", locale, copy)
			}
		}
	}
}
