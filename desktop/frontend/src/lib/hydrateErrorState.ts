/** Preserve transcript on failed hydrate instead of painting a successful empty session. */
export function applyHydrateErrorState<
  TItem,
  S extends {
    items: TItem[];
    hydratePlaceholderItems?: TItem[];
    hydrateHistoryLoaded?: boolean;
  },
>(s: S, reason: string, error: string): S & {
  hydrating: false;
  hydrateReason: string;
  hydrateError: string;
  hydrateHistoryLoaded?: boolean;
  hydratePlaceholderItems: undefined;
} {
  const keptItems = s.items.length > 0
    ? s.items
    : (s.hydratePlaceholderItems?.length ? s.hydratePlaceholderItems : s.items);
  return {
    ...s,
    items: keptItems,
    hydrating: false,
    hydrateReason: reason,
    hydrateError: error,
    hydrateHistoryLoaded: s.hydrateHistoryLoaded || keptItems.length > 0 || undefined,
    hydratePlaceholderItems: undefined,
  };
}

/** The recovery detail for a failed history read: the summary, then the reader's own identity and cause. */
export function hydrateFailureDetail(summary: string, cause: unknown): string {
  const message = (cause instanceof Error ? cause.message : typeof cause === "string" ? cause : "").trim();
  const failure = cause as { stage?: unknown; reason?: unknown } | undefined;
  const identity = cause instanceof Error && typeof failure?.stage === "string" && typeof failure.reason === "string"
    ? `${failure.stage}.${failure.reason}` : "";
  const line = [identity, message].filter(Boolean).join(": ");
  return line ? `${summary}\n${line}` : summary;
}

export function hydratePlaceholderItems<TItem>(
  optionsItems: TItem[] | undefined,
): TItem[] | undefined {
  return optionsItems?.length ? optionsItems : undefined;
}
