import "../i18n/admin";
import { apiRequest } from "./client";
import type {
  ConfigurationCatalog,
  ConfigurationField,
  ConfigurationGroup,
  ConfigurationUpdateResponse,
  ConfigurationValue
} from "./generated";

export type {
  ConfigurationCatalog,
  ConfigurationField,
  ConfigurationGroup,
  ConfigurationUpdateResponse,
  ConfigurationValue
} from "./generated";

export const configurationQueryKey = ["configuration-catalog"] as const;

export async function readConfiguration(signal?: AbortSignal): Promise<ConfigurationCatalog> {
  return apiRequest<ConfigurationCatalog>("/admin/api/settings/configuration", { signal });
}

export function saveConfiguration(values: Record<string, unknown>, csrfToken: string) {
  return apiRequest<ConfigurationUpdateResponse>("/admin/api/settings/configuration", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ confirm: "save", values })
  });
}
