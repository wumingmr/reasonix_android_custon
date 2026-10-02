package boot

import (
	"fmt"
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/event"
)

func emitUserConfigUpgradeNotice(sink event.Sink, cfg *config.Config, deepSeekProtocolMigrated bool, deepSeekProtocolMigErr error, endpointRepairs []config.ProviderEndpointRepair) {
	if len(endpointRepairs) > 0 {
		text, detail := providerEndpointRepairNotice(cfg, endpointRepairs)
		sink.Emit(event.Event{
			Kind:   event.Notice,
			Level:  event.LevelInfo,
			Text:   text,
			Detail: detail,
		})
		return
	}
	if deepSeekProtocolMigrated {
		detail := cfg.OpenCodeGoUpgradeSummary()
		if detail == "" {
			detail = "Legacy built-in DeepSeek defaults now use Chat Completions with independent web search."
		}
		sink.Emit(event.Event{
			Kind:   event.Notice,
			Level:  event.LevelInfo,
			Text:   "User configuration was upgraded.",
			Detail: detail + " Protocol changes start a new provider cache prefix; later requests rebuild normal prefix-cache reuse.",
		})
	} else if deepSeekProtocolMigErr != nil {
		sink.Emit(event.Event{
			Kind:   event.Notice,
			Level:  event.LevelWarn,
			Text:   "DeepSeek protocol migration did not complete.",
			Detail: deepSeekProtocolMigErr.Error(),
		})
	}
}

func providerEndpointRepairNotice(cfg *config.Config, repairs []config.ProviderEndpointRepair) (string, string) {
	lang := ""
	if cfg != nil {
		lang = cfg.DesktopLanguage()
		if lang == "" {
			lang = cfg.Language
		}
	}
	chinese := strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "zh")
	details := make([]string, 0, len(repairs))
	for _, repair := range repairs {
		if chinese {
			details = append(details, fmt.Sprintf(
				"已自动修复供应商连接“%s”：协议由 %s 调整为 %s；请求地址为 %s",
				repair.ProviderName,
				providerProtocolNoticeLabel(repair.FromProtocol),
				providerProtocolNoticeLabel(repair.ToProtocol),
				repair.RequestURL,
			))
			continue
		}
		details = append(details, fmt.Sprintf(
			"%s: protocol %s → %s; request URL %s",
			repair.ProviderName,
			providerProtocolNoticeLabel(repair.FromProtocol),
			providerProtocolNoticeLabel(repair.ToProtocol),
			repair.RequestURL,
		))
	}
	if chinese {
		return "已自动修复供应商连接设置。", strings.Join(details, "\n") + "。协议变更会启用新的供应商缓存前缀，后续请求会重新建立正常的前缀缓存复用。"
	}
	return "Provider connection settings were repaired.", strings.Join(details, "\n") + ". Protocol changes start a new provider cache prefix; later requests rebuild normal prefix-cache reuse."
}

func providerProtocolNoticeLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "anthropic":
		return "Anthropic Messages"
	case "responses", "dashscope-responses":
		return "Responses"
	case "openai":
		return "OpenAI Chat Completions"
	default:
		return strings.TrimSpace(kind)
	}
}
