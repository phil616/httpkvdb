import { lazy, Suspense, useMemo, useState } from "react";
import { App as AntApp, ConfigProvider, Grid, Spin, theme } from "antd";
import zhCN from "antd/locale/zh_CN";
import { ApiClient, clearPersistedSession, initialSession, persistSession } from "../lib/api";
import { SessionState } from "../lib/types";
import { ConsoleLayout } from "./ConsoleLayout";
import { ViewKey } from "./types";

const { useBreakpoint } = Grid;

const AdminPanel = lazy(() => import("../features/admin/AdminPanel").then((module) => ({ default: module.AdminPanel })));
const KvPanel = lazy(() => import("../features/kv/KvPanel").then((module) => ({ default: module.KvPanel })));
const TxPanel = lazy(() => import("../features/tx/TxPanel").then((module) => ({ default: module.TxPanel })));
const ImportExportPanel = lazy(() => import("../features/import-export/ImportExportPanel").then((module) => ({ default: module.ImportExportPanel })));

const viewTitles: Record<ViewKey, string> = {
  admin: "管理面板",
  kv: "用户 KV",
  tx: "事务工作台",
  import: "导入导出"
};

export function AppRoot() {
  return (
    <ConfigProvider
      locale={zhCN}
      theme={{
        algorithm: theme.defaultAlgorithm,
        token: {
          colorPrimary: "#256f7f",
          borderRadius: 6,
          fontFamily: "Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif"
        },
        components: {
          Layout: {
            bodyBg: "#eef2f6",
            siderBg: "#101827"
          },
          Card: {
            borderRadiusLG: 8
          }
        }
      }}
    >
      <AntApp>
        <ConsoleApp />
      </AntApp>
    </ConfigProvider>
  );
}

function ConsoleApp() {
  const [session, setSession] = useState<SessionState>(() => initialSession());
  const [activeView, setActiveView] = useState<ViewKey>("admin");
  const screens = useBreakpoint();
  const client = useMemo(() => new ApiClient(session), [session]);

  function handleSession(next: SessionState) {
    const normalized = { ...next, apiBaseUrl: next.apiBaseUrl.replace(/\/$/, "") };
    persistSession(normalized);
    setSession(normalized);
  }

  function resetCredential() {
    clearPersistedSession();
    setSession((current) => ({ ...current, credential: "", rememberCredential: false }));
  }

  return (
    <ConsoleLayout
      activeView={activeView}
      apiBaseUrl={session.apiBaseUrl}
      isCompact={!screens.lg}
      onClearCredential={resetCredential}
      onSessionChange={handleSession}
      onViewChange={setActiveView}
      session={session}
      title={viewTitles[activeView]}
    >
      <Suspense fallback={<Spin className="view-spinner" size="large" />}>
        {activeView === "admin" && <AdminPanel apiBaseUrl={session.apiBaseUrl} client={client} />}
        {activeView === "kv" && <KvPanel client={client} />}
        {activeView === "tx" && <TxPanel client={client} />}
        {activeView === "import" && <ImportExportPanel client={client} />}
      </Suspense>
    </ConsoleLayout>
  );
}
