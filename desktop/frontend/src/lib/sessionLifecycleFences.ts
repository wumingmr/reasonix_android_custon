import type { ProjectNode } from "./types";
import { projectSessionKeys } from "./projectSessionIdentity";

type Fence = { generation: number; operationId: string; aliases: readonly string[] };

/** Application-owned: remounting the sidebar cannot revive a committed archive.
 * An active directory row with a newer lifecycle proves explicit restoration.
 * Runtime revisions cannot establish that proof and never release this fence.
 */
export function createSessionLifecycleFences() {
  const fences = new Map<string, Fence>();
  return {
    archive(node: ProjectNode, receipt: { lifecycleGeneration: number; operationId: string; identityAliases?: string[] }) {
      const aliases = [...new Set([...projectSessionKeys(node), ...(receipt.identityAliases ?? [])])];
      const fence = { generation: receipt.lifecycleGeneration, operationId: receipt.operationId, aliases };
      for (const key of aliases) {
        if ((fences.get(key)?.generation ?? -1) <= fence.generation) fences.set(key, fence);
      }
    },
    observeDirectory(nodes: readonly ProjectNode[]) {
      const visit = (node: ProjectNode) => {
        for (const key of projectSessionKeys(node)) {
          const fence = fences.get(key);
          if (fence && !node.runtimeOnly && (node.lifecycleGeneration ?? 0) > fence.generation) {
            for (const alias of fence.aliases) if (fences.get(alias) === fence) fences.delete(alias);
          }
        }
        node.children?.forEach(visit);
      };
      nodes.forEach(visit);
    },
    keys(): ReadonlySet<string> { return new Set(fences.keys()); },
  };
}

export const sessionLifecycleFences = createSessionLifecycleFences();
