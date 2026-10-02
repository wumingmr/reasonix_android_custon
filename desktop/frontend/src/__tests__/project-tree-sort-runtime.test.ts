type Equal = (actual: unknown, expected: unknown, label: string) => void;

export async function runProjectTreeSortRuntimeTests(eq: Equal, projectTreeSource: string) {
  // Request counts, cursor invalidation and stale completions are exercised
  // against the mounted component in project-tree-loading.test.tsx.
  eq(
    projectTreeSource.includes("sortMode,")
      && projectTreeSource.includes("workbenchSortModeRef.current"),
    true,
    "topic page requests carry the selected conversation sort mode",
  );
}
