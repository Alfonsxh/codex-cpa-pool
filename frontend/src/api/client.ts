import { t, getLanguage } from "../i18n";
export type ApiErrorBody = {
  error?: {
    code?: string;
    message_key?: string;
    message_params?: Record<string, unknown>;
    message?: string;
    type?: string;
  };
};

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly retryAfterSeconds: number;

  constructor(status: number, code: string, message: string, retryAfterSeconds = 0) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

export type UnauthorizedEvent = {
  path: string;
  scope: "admin" | "portal" | "unknown";
  code: string;
  message: string;
};

const unauthorizedListeners = new Set<(event: UnauthorizedEvent) => void>();

export function subscribeUnauthorized(listener: (event: UnauthorizedEvent) => void) {
  unauthorizedListeners.add(listener);
  return () => {
    unauthorizedListeners.delete(listener);
  };
}

export async function apiRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await apiResponse(path, init);
  const payload: unknown = await response.json();
  return payload as T;
}

// Binary exports share session-expiry handling with ordinary JSON requests.
export async function apiResponse(path: string, init: RequestInit = {}): Promise<Response> {
  const headers: Record<string, string> = init.headers instanceof Headers || Array.isArray(init.headers)
    ? Object.fromEntries(new Headers(init.headers).entries()) : { ...init.headers };
  const supplied = new Headers(headers);
  if (!supplied.has("Accept")) headers.Accept = "application/json";
  if (init.body && !supplied.has("Content-Type")) headers["Content-Type"] = "application/json";
  for (const key of Object.keys(headers)) if (key.toLowerCase() === "accept-language") delete headers[key];
  headers["Accept-Language"] = getLanguage();
  const response = await fetch(path, {
    ...init,
    credentials: "same-origin",
    headers
  });
  if (!response.ok) {
    let payload: ApiErrorBody = {};
    try {
      payload = (await response.json()) as ApiErrorBody;
    } catch {
      // A proxy or interrupted response may not contain the API error envelope.
    }
    const error = new ApiError(
      response.status,
      payload.error?.code ?? "request_failed",
      payload.error?.message ?? t("common.request_failed_http", [response.status]),
      parseRetryAfterSeconds(response.headers.get("Retry-After"))
    );
    if (response.status === 401) {
      const event: UnauthorizedEvent = {
        path,
        code: error.code,
        message: error.message,
        scope: path.startsWith("/admin/api/")
          ? "admin"
          : path.startsWith("/usage/")
            ? "portal"
            : "unknown"
      };
      unauthorizedListeners.forEach((listener) => listener(event));
    }
    throw error;
  }
  return response;
}

function parseRetryAfterSeconds(value: string | null): number {
  if (!value) return 0;
  const seconds = Number.parseInt(value, 10);
  return Number.isFinite(seconds) ? Math.max(0, Math.min(seconds, 3_600)) : 0;
}
