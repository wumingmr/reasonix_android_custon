import type { WireExtensionForm, WireExtensionSurface } from "./types";

export interface ExtensionStatusEntry {
  pluginId: string;
  surfaceId: string;
  label: string;
  detail?: string;
  severity?: string;
  progress?: number;
  generation?: number;
}

export interface ExtensionFormState {
  pluginId: string;
  surfaceId: string;
  sessionId?: string;
  generation?: number;
  formInstanceId: string;
  formInstanceExact: boolean;
  form: WireExtensionForm;
}

export interface ExtensionNotificationEntry {
  id: string;
  pluginId: string;
  title: string;
  body?: string;
  severity?: string;
}

export function extensionSurfaceKey(surface: Pick<WireExtensionSurface, "pluginId" | "surfaceId">): string {
  return `${surface.pluginId}:${surface.surfaceId}`;
}

export function acceptsExtensionGeneration(stored: number | undefined, incoming: number | undefined): boolean {
  return incoming === undefined || stored === undefined || incoming >= stored;
}

export function applyExtensionForm<T extends { extensionForm?: ExtensionFormState }>(s: T, surface: WireExtensionSurface): T {
  if (!surface.form) return s;
  return {
    ...s,
    extensionForm: {
      pluginId: surface.pluginId,
      surfaceId: surface.surfaceId,
      sessionId: surface.sessionId,
      generation: surface.generation,
      formInstanceId: surface.formInstanceId ?? JSON.stringify([surface.pluginId, surface.surfaceId, surface.generation ?? 0]),
      formInstanceExact: Boolean(surface.formInstanceId),
      form: surface.form,
    },
  };
}
