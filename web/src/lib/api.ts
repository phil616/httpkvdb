import { AdminKeyInfo, ApiError, CreatedUserspace, ImportResult, KvMetadata, SessionState, TxResult, TxStatus, UserspaceInfo } from "./types";

const defaultApiBaseUrl = import.meta.env.VITE_API_BASE_URL || "http://127.0.0.1:8080";
const sessionStorageKey = "httpkvdb.console.session";

export function initialSession(): SessionState {
  const fallback: SessionState = {
    apiBaseUrl: defaultApiBaseUrl.replace(/\/$/, ""),
    authMode: "ApiKey",
    credential: "",
    rememberCredential: false
  };
  try {
    const raw = window.localStorage.getItem(sessionStorageKey);
    if (!raw) {
      return fallback;
    }
    const stored = JSON.parse(raw) as Partial<SessionState>;
    if (!stored.apiBaseUrl || !stored.authMode || !stored.credential) {
      return fallback;
    }
    return {
      apiBaseUrl: stored.apiBaseUrl.replace(/\/$/, ""),
      authMode: stored.authMode,
      credential: stored.credential,
      rememberCredential: true
    };
  } catch {
    return fallback;
  }
}

export function persistSession(session: SessionState): void {
  try {
    if (!session.rememberCredential || !session.credential.trim()) {
      window.localStorage.removeItem(sessionStorageKey);
      return;
    }
    window.localStorage.setItem(
      sessionStorageKey,
      JSON.stringify({
        apiBaseUrl: session.apiBaseUrl.replace(/\/$/, ""),
        authMode: session.authMode,
        credential: session.credential
      })
    );
  } catch {
    // Browser storage can be disabled; the in-memory session still works.
  }
}

export function clearPersistedSession(): void {
  try {
    window.localStorage.removeItem(sessionStorageKey);
  } catch {
    // Browser storage can be disabled; clearing the in-memory session is enough.
  }
}

export class ApiClient {
  private session: SessionState;

  constructor(session: SessionState) {
    this.session = { ...session, apiBaseUrl: session.apiBaseUrl.replace(/\/$/, "") };
  }

  async health(path: "/healthz" | "/readyz" | "/metrics"): Promise<string> {
    const response = await fetch(`${this.session.apiBaseUrl}${path}`);
    const text = await response.text();
    if (!response.ok) {
      throw new ApiError(response.status, text);
    }
    return text;
  }

  async putKey(key: string, value: Blob | string, contentType: string, userspace?: string): Promise<KvMetadata> {
    const response = await this.request(this.kvPath(key, userspace), {
      method: "PUT",
      headers: { "Content-Type": contentType },
      body: value
    });
    return metadataFromHeaders(response.headers);
  }

  async getKey(key: string, userspace?: string): Promise<{ value: ArrayBuffer; metadata: KvMetadata }> {
    const response = await this.request(this.kvPath(key, userspace), { method: "GET" });
    return { value: await response.arrayBuffer(), metadata: metadataFromHeaders(response.headers) };
  }

  async headKey(key: string, userspace?: string): Promise<KvMetadata> {
    const response = await this.request(this.kvPath(key, userspace), { method: "HEAD" });
    return metadataFromHeaders(response.headers);
  }

  async deleteKey(key: string, userspace?: string): Promise<void> {
    await this.request(this.kvPath(key, userspace), { method: "DELETE" });
  }

  async createUserspace(userspaceId: string, userId: string): Promise<CreatedUserspace> {
    return this.json<CreatedUserspace>("/v1/admin/userspaces", {
      method: "POST",
      body: JSON.stringify({ userspace_id: userspaceId, user_id: userId || undefined })
    });
  }

  async listUserspaces(): Promise<UserspaceInfo[]> {
    return this.json<UserspaceInfo[]>("/v1/admin/userspaces", { method: "GET" });
  }

  async deleteUserspace(userspaceId: string): Promise<void> {
    await this.request(`/v1/admin/userspaces/${encodeURIComponent(userspaceId)}`, { method: "DELETE" });
  }

  async rotateUserspaceAPIKey(userspaceId: string): Promise<CreatedUserspace> {
    return this.json<CreatedUserspace>(`/v1/admin/userspaces/${encodeURIComponent(userspaceId)}/api-key`, { method: "POST" });
  }

  async listAdminKeys(userspaceId: string): Promise<AdminKeyInfo[]> {
    return this.json<AdminKeyInfo[]>(`/v1/admin/userspaces/${encodeURIComponent(userspaceId)}/keys`, { method: "GET" });
  }

  async putAdminKey(userspaceId: string, key: string, value: Blob | string, contentType: string): Promise<KvMetadata> {
    const response = await this.request(this.adminKvPath(userspaceId, key), {
      method: "PUT",
      headers: { "Content-Type": contentType },
      body: value
    });
    return metadataFromHeaders(response.headers);
  }

  async getAdminKey(userspaceId: string, key: string): Promise<{ value: ArrayBuffer; metadata: KvMetadata }> {
    const response = await this.request(this.adminKvPath(userspaceId, key), { method: "GET" });
    return { value: await response.arrayBuffer(), metadata: metadataFromHeaders(response.headers) };
  }

  async headAdminKey(userspaceId: string, key: string): Promise<KvMetadata> {
    const response = await this.request(this.adminKvPath(userspaceId, key), { method: "HEAD" });
    return metadataFromHeaders(response.headers);
  }

  async deleteAdminKey(userspaceId: string, key: string): Promise<void> {
    await this.request(this.adminKvPath(userspaceId, key), { method: "DELETE" });
  }

  async createTx(txId: string, totalOps: number, timeoutMs: number): Promise<TxStatus> {
    return this.json<TxStatus>("/v1/tx", {
      method: "POST",
      body: JSON.stringify({ tx_id: txId || undefined, total_ops: totalOps, timeout_ms: timeoutMs })
    });
  }

  async addTxOp(txId: string, seq: number, op: string, key: string, opId: string, contentType: string, body: string): Promise<TxStatus | TxResult> {
    const headers: Record<string, string> = {
      "X-KV-Op": op,
      "X-KV-Key": key,
      "X-KV-Op-Id": opId
    };
    let requestBody: string | undefined;
    if (op === "PUT") {
      headers["Content-Type"] = contentType;
      requestBody = body;
    }
    return this.json<TxStatus | TxResult>(`/v1/tx/${encodeURIComponent(txId)}/ops/${seq}`, {
      method: "POST",
      headers,
      body: requestBody
    });
  }

  async commitTx(txId: string, totalOps: number, txDigest?: string): Promise<TxStatus | TxResult> {
    return this.json<TxStatus | TxResult>(`/v1/tx/${encodeURIComponent(txId)}/commit`, {
      method: "POST",
      body: JSON.stringify({ total_ops: totalOps, tx_digest: txDigest || undefined })
    });
  }

  async getTxResult(txId: string): Promise<TxStatus | TxResult> {
    return this.json<TxStatus | TxResult>(`/v1/tx/${encodeURIComponent(txId)}/result`, { method: "GET" });
  }

  async abortTx(txId: string): Promise<TxStatus> {
    return this.json<TxStatus>(`/v1/tx/${encodeURIComponent(txId)}/abort`, { method: "POST" });
  }

  async exportData(): Promise<Blob> {
    const response = await this.request("/v1/export", {
      method: "GET",
      headers: { Accept: "application/octet-stream" }
    });
    return response.blob();
  }

  async importData(file: File, mode: "replace" | "merge-overwrite" | "merge-skip"): Promise<ImportResult> {
    return this.json<ImportResult>("/v1/import", {
      method: "POST",
      headers: {
        "Content-Type": "application/octet-stream",
        "X-KV-Import-Mode": mode
      },
      body: file
    });
  }

  private async json<T>(path: string, init: RequestInit): Promise<T> {
    const headers = new Headers(init.headers);
    if (init.body && !headers.has("Content-Type")) {
      headers.set("Content-Type", "application/json");
    }
    const response = await this.request(path, { ...init, headers });
    return response.json() as Promise<T>;
  }

  private kvPath(key: string, userspace?: string): string {
    const encodedKey = encodeURIComponent(key);
    const trimmedUserspace = userspace?.trim();
    if (trimmedUserspace) {
      return `/api/v1/${encodeURIComponent(trimmedUserspace)}/${encodedKey}`;
    }
    return `/v1/kv/${encodedKey}`;
  }

  private adminKvPath(userspaceId: string, key: string): string {
    return `/v1/admin/userspaces/${encodeURIComponent(userspaceId)}/kv/${encodeURIComponent(key)}`;
  }

  private async request(path: string, init: RequestInit): Promise<Response> {
    const headers = new Headers(init.headers);
    if (this.session.credential) {
      if (this.session.authMode === "ApiKey") {
        headers.set("APIKey", this.session.credential);
      } else {
        headers.set("Authorization", `Bearer ${this.session.credential}`);
      }
    }
    const response = await fetch(`${this.session.apiBaseUrl}${path}`, { ...init, headers });
    if (!response.ok) {
      let payload: unknown;
      try {
        payload = await response.json();
      } catch {
        payload = await response.text();
      }
      throw new ApiError(response.status, payload as never);
    }
    return response;
  }
}

export function metadataFromHeaders(headers: Headers): KvMetadata {
  return {
    version: headers.get("X-KV-Version") || undefined,
    size: headers.get("X-KV-Size") || undefined,
    checksum: headers.get("X-KV-Checksum") || undefined,
    contentType: headers.get("Content-Type") || undefined
  };
}

export function decodeText(buffer: ArrayBuffer, contentType?: string): string {
  if (contentType?.startsWith("application/octet-stream")) {
    const bytes = new Uint8Array(buffer);
    return `binary:${bytes.length} bytes`;
  }
  return new TextDecoder().decode(buffer);
}

export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}
