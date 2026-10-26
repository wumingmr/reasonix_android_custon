import type { ProviderView } from "./types";

/** Prefer the exact model's resolved contract, including an empty option list. */
export function providerModelReasoningLevels(provider: ProviderView | undefined, model: string): string[] {
  if (!provider) return [];
  const reasoning = provider.modelCapabilities?.find(item => item.model === model)?.reasoning;
  if (reasoning) return reasoning.options.map(option => option.id);

  // Older hosts may omit resolved metadata. Keep their explicit declarations
  // usable without overriding a newer host's unsupported/unknown capability.
  const override = provider.modelOverrides?.find(item => item.model === model);
  if ((override?.reasoningProtocol || provider.reasoningProtocol) === "none") return [];
  return override?.supportedEfforts?.length ? override.supportedEfforts : provider.supportedEfforts ?? [];
}
