export interface ProviderModelCapabilityView {
  reasoning?: { state?: "supported" | "unsupported" | "unknown" | string; options: { id: string; name: string; description?: string }[]; default?: string; error?: string };
	automaticState?: string;
	automaticSource?: string;
	imageInputEnableAllowed?: boolean;
	imageInputBlockReason?: string;
  model: string;
  inputModalities: string[];
  state: "supported" | "unsupported" | "unknown" | string;
  source: string;
}
