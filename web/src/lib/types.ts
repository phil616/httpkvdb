export type AuthMode = "ApiKey" | "Bearer";

export interface SessionState {
  apiBaseUrl: string;
  authMode: AuthMode;
  credential: string;
}

export interface ApiErrorPayload {
  error: string;
  message: string;
  request_id?: string;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId?: string;

  constructor(status: number, payload: ApiErrorPayload | string) {
    const body = typeof payload === "string" ? { error: "HTTP_ERROR", message: payload } : payload;
    super(body.message || body.error);
    this.status = status;
    this.code = body.error;
    this.requestId = body.request_id;
  }
}

export interface KvMetadata {
  version?: string;
  size?: string;
  checksum?: string;
  contentType?: string;
}

export interface TxStatus {
  tx_id: string;
  status: string;
  total_ops?: number;
  deadline?: string;
  received_seq?: number[];
  missing_seq?: number[];
}

export interface TxOperationResult {
  seq: number;
  op: string;
  status: number;
  key: string;
  content_type?: string;
  value_base64?: string;
  version?: number;
  error?: string;
}

export interface TxResult {
  tx_id: string;
  status: string;
  results?: TxOperationResult[];
}

export interface ImportResult {
  imported: number;
  skipped: number;
  replaced: number;
}

export interface TxDraftOp {
  seq: number;
  op: "GET" | "PUT" | "DELETE" | "EXISTS" | "HEAD";
  key: string;
  opId: string;
  contentType: string;
  body: string;
}

