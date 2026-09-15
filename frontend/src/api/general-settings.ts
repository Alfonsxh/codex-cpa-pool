import { t } from "../i18n";
import { apiRequest } from "./client";
import type {
  BrandingLogoMutationResponse,
  GeneralSettings,
  GeneralSettingsValues,
  ManagementKeyRotationResponse
} from "./generated";

export type { GeneralSettings, GeneralSettingsValues } from "./generated";

export const generalSettingsQueryKey = ["general-settings"] as const;

export function readGeneralSettings(signal?: AbortSignal): Promise<GeneralSettings> {
  return apiRequest<GeneralSettings>("/admin/api/settings/general", { signal });
}

export function saveGeneralSettings(values: GeneralSettingsValues, csrfToken: string) {
  return apiRequest<{ message: string; settings: GeneralSettings }>("/admin/api/settings/general", {
    method: "PUT",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ confirm: "save", values })
  });
}

export function saveInitialPassword(initialPassword: string, confirmation: string, csrfToken: string) {
  return apiRequest<{ message: string; configured: true }>("/admin/api/settings/initial-password", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ initial_password: initialPassword, confirmation })
  });
}

export async function saveBrandingLogo(file: File, csrfToken: string) {
  const dataBase64 = await readFileBase64(file);
  return apiRequest<BrandingLogoMutationResponse>("/admin/api/settings/logo", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({
      filename: file.name,
      content_type: file.type,
      data_base64: dataBase64,
      confirm: "save"
    })
  });
}

export function resetBrandingLogo(csrfToken: string) {
  return apiRequest<BrandingLogoMutationResponse>("/admin/api/settings/logo", {
    method: "DELETE",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ confirm: "reset" })
  });
}

export function rotateManagementKey(newKey: string, confirmation: string, csrfToken: string) {
  return apiRequest<ManagementKeyRotationResponse>("/admin/api/settings/management-key", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ new_key: newKey, confirmation })
  });
}

function readFileBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error(t("common.unable_to_read_the_logo_file")));
    reader.onload = () => {
      const value = typeof reader.result === "string" ? reader.result : "";
      const separator = value.indexOf(",");
      if (separator < 0) {
        reject(new Error(t("common.unable_to_encode_the_logo_file")));
        return;
      }
      resolve(value.slice(separator + 1));
    };
    reader.readAsDataURL(file);
  });
}
