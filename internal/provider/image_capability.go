package provider

// ResolveImageInputMetadata preserves unknown separately from declared text-only.
// Unknown metadata permits serialization of explicitly attached images; boot
// still decides whether an image-understanding fallback owns those attachments.
func ResolveImageInputMetadata(model string, metadata *ModelInfo, legacy bool) (ModelInfo, bool) {
	if metadata == nil {
		info := ModelInfo{ID: model, InputModalities: []ModelModality{ModalityText}}
		if legacy {
			info.InputModalities = append(info.InputModalities, ModalityImage)
		}
		return info, legacy
	}
	info := *metadata
	info.ID = model
	info.InputModalities = append([]ModelModality(nil), metadata.InputModalities...)
	return info, metadata.InputModalities == nil || metadata.SupportsInput(ModalityImage)
}
