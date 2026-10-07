package imageinput

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"reasonix/internal/provider"
)

// ErrNoModel means no image understanding model can be chosen for a model that
// cannot read images itself.
var ErrNoModel = errors.New("no image understanding model is configured")

func (s *Service) selectModel(current string, images []string) (string, error) {
	if s == nil || s.config.Model == "" {
		return "", ErrNoModel
	}
	target := s.config.Model
	if target == "auto" {
		if s.config.Select == nil {
			return "", fmt.Errorf("image model selection is unavailable")
		}
		var ok bool
		target, ok = s.config.Select(current, target)
		if !ok || strings.TrimSpace(target) == "" {
			return "", fmt.Errorf("%w: 当前服务商没有可用的图片理解模型，请在设置中显式选择。", ErrNoModel)
		}
	}
	from, _, fromOK := strings.Cut(current, "/")
	to, _, toOK := strings.Cut(target, "/")
	if (!fromOK || !toOK || from != to) && slices.ContainsFunc(images, provider.IsImageFileID) {
		return "", fmt.Errorf("来源服务商的 file id 不能跨服务商复用")
	}
	return target, nil
}
