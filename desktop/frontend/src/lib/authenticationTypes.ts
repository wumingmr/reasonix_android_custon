export interface ConnectionAuthentication {
  status: "ready" | "missing_credential" | "authentication_rejected" | "credential_store_unavailable" | string;
  providerName?: string;
  modelRef?: string;
  keyEnv?: string;
  httpStatus?: number;
  code?: string;
  message?: string;
}

export function modelSettingsAllowSubmission(ready: boolean, state?: {
  authentication?: ConnectionAuthentication;
  modelSettingsPending?: boolean;
}): boolean {
  return ready && (state?.modelSettingsPending === true || (state?.authentication?.status ?? "ready") === "ready");
}
