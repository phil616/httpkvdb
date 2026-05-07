import React, { useEffect, useMemo, useState } from "react";
import ReactDOM from "react-dom/client";
import {
  Activity,
  Database,
  FileDown,
  FileUp,
  KeyRound,
  LayoutDashboard,
  Lock,
  LogOut,
  Play,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2
} from "lucide-react";
import { ApiClient, clearPersistedSession, decodeText, downloadBlob, initialSession, persistSession } from "./lib/api";
import { ApiError, ImportResult, KvMetadata, SessionState, TxDraftOp, TxResult, TxStatus } from "./lib/types";
import "./styles.css";

type View = "admin" | "kv" | "tx" | "import";

const contentTypes = ["application/json", "text/plain", "application/octet-stream"];

function App() {
  const [session, setSession] = useState<SessionState>(() => initialSession());
  const [activeView, setActiveView] = useState<View>("admin");
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const client = useMemo(() => new ApiClient(session), [session]);
  const authenticated = Boolean(session.credential.trim());

  function handleSession(next: SessionState) {
    const normalized = { ...next, apiBaseUrl: next.apiBaseUrl.replace(/\/$/, "") };
    setError("");
    setNotice(normalized.rememberCredential && normalized.credential.trim() ? "会话已更新，凭据已保存" : "会话已更新");
    persistSession(normalized);
    setSession(normalized);
  }

  function resetCredential() {
    clearPersistedSession();
    setSession((current) => ({ ...current, credential: "", rememberCredential: false }));
    setNotice("认证凭据已从当前页面和本地存储清除");
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <Database size={28} />
          <div>
            <strong>httpkvdb</strong>
            <span>Console</span>
          </div>
        </div>
        <nav className="nav-list" aria-label="主导航">
          <NavButton icon={<LayoutDashboard size={18} />} label="管理面板" active={activeView === "admin"} onClick={() => setActiveView("admin")} />
          <NavButton icon={<KeyRound size={18} />} label="用户 KV" active={activeView === "kv"} onClick={() => setActiveView("kv")} />
          <NavButton icon={<Play size={18} />} label="事务工作台" active={activeView === "tx"} onClick={() => setActiveView("tx")} />
          <NavButton icon={<FileUp size={18} />} label="导入导出" active={activeView === "import"} onClick={() => setActiveView("import")} />
        </nav>
        <div className="session-summary">
          <span>后端</span>
          <strong>{session.apiBaseUrl}</strong>
          <span>认证</span>
          <strong>{authenticated ? session.authMode : "未配置"}</strong>
          <span>本地凭据</span>
          <strong>{session.rememberCredential && authenticated ? "已保存" : "未保存"}</strong>
          <button className="ghost-button" onClick={resetCredential}>
            <LogOut size={16} /> 清除凭据
          </button>
        </div>
      </aside>
      <main className="workspace">
        <header className="topbar">
          <div>
            <p className="eyebrow">Single-node Serializable KV</p>
            <h1>{viewTitle(activeView)}</h1>
          </div>
          <SessionPanel session={session} onChange={handleSession} />
        </header>
        {(notice || error) && (
          <div className={error ? "alert error" : "alert success"} role="status">
            {error || notice}
          </div>
        )}
        <section className="view-surface">
          {activeView === "admin" && <AdminPanel client={client} apiBaseUrl={session.apiBaseUrl} onError={setError} onNotice={setNotice} />}
          {activeView === "kv" && <KvPanel client={client} onError={setError} onNotice={setNotice} />}
          {activeView === "tx" && <TxPanel client={client} onError={setError} onNotice={setNotice} />}
          {activeView === "import" && <ImportExportPanel client={client} onError={setError} onNotice={setNotice} />}
        </section>
      </main>
    </div>
  );
}

function viewTitle(view: View) {
  switch (view) {
    case "admin":
      return "管理面板";
    case "kv":
      return "用户面板";
    case "tx":
      return "事务工作台";
    case "import":
      return "导入导出";
  }
}

function NavButton({ icon, label, active, onClick }: { icon: React.ReactNode; label: string; active: boolean; onClick: () => void }) {
  return (
    <button className={`nav-button ${active ? "active" : ""}`} onClick={onClick}>
      {icon}
      <span>{label}</span>
    </button>
  );
}

function SessionPanel({ session, onChange }: { session: SessionState; onChange: (session: SessionState) => void }) {
  const [draft, setDraft] = useState(session);

  useEffect(() => {
    setDraft(session);
  }, [session]);

  return (
    <form
      className="session-panel"
      onSubmit={(event) => {
        event.preventDefault();
        onChange(draft);
      }}
    >
      <label>
        <span>后端地址</span>
        <input value={draft.apiBaseUrl} onChange={(event) => setDraft({ ...draft, apiBaseUrl: event.target.value })} />
      </label>
      <label>
        <span>认证方式</span>
        <select value={draft.authMode} onChange={(event) => setDraft({ ...draft, authMode: event.target.value as SessionState["authMode"] })}>
          <option value="ApiKey">APIKey</option>
          <option value="Bearer">JWT Bearer</option>
        </select>
      </label>
      <label>
        <span>凭据</span>
        <input type="password" value={draft.credential} onChange={(event) => setDraft({ ...draft, credential: event.target.value })} autoComplete="off" />
      </label>
      <label className="checkbox-label">
        <input
          type="checkbox"
          checked={draft.rememberCredential}
          onChange={(event) => setDraft({ ...draft, rememberCredential: event.target.checked })}
        />
        <span>
          <Lock size={15} /> 保存凭据
        </span>
      </label>
      <button className="primary-button" type="submit">
        <ShieldCheck size={16} /> 应用
      </button>
    </form>
  );
}

function AdminPanel({ client, apiBaseUrl, onError, onNotice }: PanelProps & { apiBaseUrl: string }) {
  const [health, setHealth] = useState("未检查");
  const [ready, setReady] = useState("未检查");
  const [metrics, setMetrics] = useState<Record<string, string>>({});

  async function refresh() {
    await runAction(onError, onNotice, "管理面板已刷新", async () => {
      const [healthText, readyText, metricsText] = await Promise.all([client.health("/healthz"), client.health("/readyz"), client.health("/metrics")]);
      setHealth(healthText.trim() || "ok");
      setReady(readyText.trim() || "ok");
      setMetrics(parseMetrics(metricsText));
    });
  }

  return (
    <div className="panel-grid">
      <section className="section-band">
        <div className="section-header">
          <div>
            <p className="eyebrow">Operations</p>
            <h2>服务状态</h2>
          </div>
          <button className="secondary-button" onClick={refresh}>
            <RefreshCw size={16} /> 刷新
          </button>
        </div>
        <div className="status-grid">
          <Metric label="API Base URL" value={apiBaseUrl} />
          <Metric label="healthz" value={health} tone={health === "ok" ? "good" : "neutral"} />
          <Metric label="readyz" value={ready} tone={ready === "ok" ? "good" : "neutral"} />
          <Metric label="requests" value={metrics.http_requests_total || "-"} />
        </div>
      </section>
      <section className="section-band">
        <div className="section-header">
          <div>
            <p className="eyebrow">Metrics</p>
            <h2>核心指标</h2>
          </div>
          <Activity size={22} />
        </div>
        <div className="metrics-table">
          {["kv_get_total", "kv_put_total", "kv_delete_total", "tx_created_total", "tx_committed_total", "tx_aborted_total", "import_total", "export_total"].map((name) => (
            <div key={name}>
              <span>{name}</span>
              <strong>{metrics[name] || "0"}</strong>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}

function KvPanel({ client, onError, onNotice }: PanelProps) {
  const [key, setKey] = useState("profile");
  const [contentType, setContentType] = useState("application/json");
  const [value, setValue] = useState('{"name":"Alice"}');
  const [metadata, setMetadata] = useState<KvMetadata>({});
  const [readValue, setReadValue] = useState("");

  async function put() {
    await runAction(onError, onNotice, "写入成功", async () => setMetadata(await client.putKey(key, value, contentType)));
  }

  async function get() {
    await runAction(onError, onNotice, "读取成功", async () => {
      const result = await client.getKey(key);
      setMetadata(result.metadata);
      setReadValue(decodeText(result.value, result.metadata.contentType));
    });
  }

  async function head() {
    await runAction(onError, onNotice, "元信息已刷新", async () => setMetadata(await client.headKey(key)));
  }

  async function remove() {
    await runAction(onError, onNotice, "删除成功", async () => {
      await client.deleteKey(key);
      setMetadata({});
      setReadValue("");
    });
  }

  return (
    <div className="two-column">
      <section className="section-band">
        <div className="section-header">
          <div>
            <p className="eyebrow">User KV Space</p>
            <h2>键值操作</h2>
          </div>
          <KeyRound size={22} />
        </div>
        <div className="form-grid">
          <label className="span-2">
            <span>Key</span>
            <input value={key} onChange={(event) => setKey(event.target.value)} />
          </label>
          <label>
            <span>Content-Type</span>
            <select value={contentType} onChange={(event) => setContentType(event.target.value)}>
              {contentTypes.map((item) => (
                <option key={item}>{item}</option>
              ))}
            </select>
          </label>
          <label className="span-2">
            <span>Value</span>
            <textarea value={value} onChange={(event) => setValue(event.target.value)} rows={10} />
          </label>
        </div>
        <div className="button-row">
          <button className="primary-button" onClick={put}>
            <Plus size={16} /> PUT
          </button>
          <button className="secondary-button" onClick={get}>
            <RefreshCw size={16} /> GET
          </button>
          <button className="secondary-button" onClick={head}>
            <Activity size={16} /> HEAD
          </button>
          <button className="danger-button" onClick={remove}>
            <Trash2 size={16} /> DELETE
          </button>
        </div>
      </section>
      <section className="section-band">
        <div className="section-header">
          <div>
            <p className="eyebrow">Response</p>
            <h2>读取结果</h2>
          </div>
        </div>
        <MetadataView metadata={metadata} />
        <pre className="output">{readValue || "尚未读取 value"}</pre>
      </section>
    </div>
  );
}

function TxPanel({ client, onError, onNotice }: PanelProps) {
  const [txId, setTxId] = useState("tx_ui_demo");
  const [totalOps, setTotalOps] = useState(2);
  const [timeoutMs, setTimeoutMs] = useState(30000);
  const [digest, setDigest] = useState("");
  const [op, setOp] = useState<TxDraftOp>({
    seq: 1,
    op: "PUT",
    key: "workflow",
    opId: "op-1",
    contentType: "text/plain",
    body: "draft"
  });
  const [result, setResult] = useState<TxStatus | TxResult | null>(null);

  const updateOp = <K extends keyof TxDraftOp>(field: K, value: TxDraftOp[K]) => setOp((current) => ({ ...current, [field]: value }));

  return (
    <div className="two-column wide-left">
      <section className="section-band">
        <div className="section-header">
          <div>
            <p className="eyebrow">Serializable Transaction</p>
            <h2>事务控制</h2>
          </div>
          <Lock size={22} />
        </div>
        <div className="form-grid">
          <label>
            <span>Tx ID</span>
            <input value={txId} onChange={(event) => setTxId(event.target.value)} />
          </label>
          <label>
            <span>Total Ops</span>
            <input type="number" min={1} value={totalOps} onChange={(event) => setTotalOps(Number(event.target.value))} />
          </label>
          <label>
            <span>Timeout ms</span>
            <input type="number" min={1} value={timeoutMs} onChange={(event) => setTimeoutMs(Number(event.target.value))} />
          </label>
          <label>
            <span>Tx Digest</span>
            <input value={digest} onChange={(event) => setDigest(event.target.value)} placeholder="可选" />
          </label>
        </div>
        <div className="button-row">
          <button className="primary-button" onClick={() => runAction(onError, onNotice, "事务已创建", async () => setResult(await client.createTx(txId, totalOps, timeoutMs)))}>
            <Plus size={16} /> 创建
          </button>
          <button className="secondary-button" onClick={() => runAction(onError, onNotice, "事务已提交", async () => setResult(await client.commitTx(txId, totalOps, digest)))}>
            <Play size={16} /> Commit
          </button>
          <button className="secondary-button" onClick={() => runAction(onError, onNotice, "结果已刷新", async () => setResult(await client.getTxResult(txId)))}>
            <RefreshCw size={16} /> Result
          </button>
          <button className="danger-button" onClick={() => runAction(onError, onNotice, "事务已中止", async () => setResult(await client.abortTx(txId)))}>
            <Trash2 size={16} /> Abort
          </button>
        </div>
        <div className="divider" />
        <h3>提交片段</h3>
        <div className="form-grid">
          <label>
            <span>Seq</span>
            <input type="number" min={1} value={op.seq} onChange={(event) => updateOp("seq", Number(event.target.value))} />
          </label>
          <label>
            <span>Op</span>
            <select value={op.op} onChange={(event) => updateOp("op", event.target.value as TxDraftOp["op"])}>
              {["PUT", "GET", "DELETE", "EXISTS", "HEAD"].map((item) => (
                <option key={item}>{item}</option>
              ))}
            </select>
          </label>
          <label>
            <span>Key</span>
            <input value={op.key} onChange={(event) => updateOp("key", event.target.value)} />
          </label>
          <label>
            <span>Op ID</span>
            <input value={op.opId} onChange={(event) => updateOp("opId", event.target.value)} />
          </label>
          <label>
            <span>Content-Type</span>
            <select value={op.contentType} onChange={(event) => updateOp("contentType", event.target.value)}>
              {contentTypes.map((item) => (
                <option key={item}>{item}</option>
              ))}
            </select>
          </label>
          <label className="span-2">
            <span>Body</span>
            <textarea disabled={op.op !== "PUT"} value={op.body} onChange={(event) => updateOp("body", event.target.value)} rows={6} />
          </label>
        </div>
        <button
          className="primary-button"
          onClick={() =>
            runAction(onError, onNotice, "事务片段已提交", async () =>
              setResult(await client.addTxOp(txId, op.seq, op.op, op.key, op.opId, op.contentType, op.body))
            )
          }
        >
          <Plus size={16} /> Add Op
        </button>
      </section>
      <section className="section-band">
        <div className="section-header">
          <div>
            <p className="eyebrow">Transaction Result</p>
            <h2>执行状态</h2>
          </div>
        </div>
        <pre className="output">{result ? JSON.stringify(result, null, 2) : "尚无事务结果"}</pre>
      </section>
    </div>
  );
}

function ImportExportPanel({ client, onError, onNotice }: PanelProps) {
  const [mode, setMode] = useState<"replace" | "merge-overwrite" | "merge-skip">("replace");
  const [file, setFile] = useState<File | null>(null);
  const [result, setResult] = useState<ImportResult | null>(null);

  async function exportData() {
    await runAction(onError, onNotice, "导出完成", async () => {
      const blob = await client.exportData();
      downloadBlob(blob, `kv-export-${Date.now()}.bin`);
    });
  }

  async function importData() {
    if (!file) {
      onError("请选择导入文件");
      return;
    }
    await runAction(onError, onNotice, "导入完成", async () => setResult(await client.importData(file, mode)));
  }

  return (
    <div className="two-column">
      <section className="section-band">
        <div className="section-header">
          <div>
            <p className="eyebrow">Binary Export</p>
            <h2>导出</h2>
          </div>
          <FileDown size={22} />
        </div>
        <p className="muted">导出当前认证 Principal 对应 userspace 的全部 KV 数据，后端会在全局锁内生成一致快照。</p>
        <button className="primary-button" onClick={exportData}>
          <FileDown size={16} /> 下载导出文件
        </button>
      </section>
      <section className="section-band">
        <div className="section-header">
          <div>
            <p className="eyebrow">Binary Import</p>
            <h2>导入</h2>
          </div>
          <FileUp size={22} />
        </div>
        <div className="form-grid">
          <label>
            <span>模式</span>
            <select value={mode} onChange={(event) => setMode(event.target.value as typeof mode)}>
              <option value="replace">replace</option>
              <option value="merge-overwrite">merge-overwrite</option>
              <option value="merge-skip">merge-skip</option>
            </select>
          </label>
          <label className="span-2">
            <span>文件</span>
            <input type="file" accept="application/octet-stream,.bin" onChange={(event) => setFile(event.target.files?.[0] || null)} />
          </label>
        </div>
        <button className="primary-button" onClick={importData}>
          <FileUp size={16} /> 上传并导入
        </button>
        <pre className="output">{result ? JSON.stringify(result, null, 2) : "尚未导入"}</pre>
      </section>
    </div>
  );
}

function Metric({ label, value, tone = "neutral" }: { label: string; value: string; tone?: "good" | "neutral" }) {
  return (
    <div className={`metric ${tone}`}>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function MetadataView({ metadata }: { metadata: KvMetadata }) {
  return (
    <div className="metadata-grid">
      <Metric label="Version" value={metadata.version || "-"} />
      <Metric label="Size" value={metadata.size || "-"} />
      <Metric label="Checksum" value={metadata.checksum || "-"} />
      <Metric label="Content-Type" value={metadata.contentType || "-"} />
    </div>
  );
}

interface PanelProps {
  client: ApiClient;
  onError: (message: string) => void;
  onNotice: (message: string) => void;
}

async function runAction(onError: (message: string) => void, onNotice: (message: string) => void, success: string, action: () => Promise<void>) {
  onError("");
  onNotice("");
  try {
    await action();
    onNotice(success);
  } catch (error) {
    if (error instanceof ApiError) {
      onError(`${error.status} ${error.code}: ${error.message}${error.requestId ? ` (${error.requestId})` : ""}`);
    } else if (error instanceof Error) {
      onError(error.message);
    } else {
      onError("未知错误");
    }
  }
}

function parseMetrics(text: string): Record<string, string> {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
    .reduce<Record<string, string>>((acc, line) => {
      const [name, value] = line.split(/\s+/);
      if (name && value && !name.startsWith("#")) {
        acc[name] = value;
      }
      return acc;
    }, {});
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
