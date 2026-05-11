import { ReactNode, useState } from "react";
import {
  CloudUploadOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  KeyOutlined,
  LogoutOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  PlayCircleOutlined
} from "@ant-design/icons";
import { Button, Layout, Menu, Space, Tag, Typography } from "antd";
import { SessionState } from "../lib/types";
import { SessionPanel } from "../components/SessionPanel";
import { ViewKey } from "./types";

const { Header, Sider, Content } = Layout;
const { Text, Title } = Typography;

interface ConsoleLayoutProps {
  activeView: ViewKey;
  apiBaseUrl: string;
  children: ReactNode;
  isCompact: boolean;
  onClearCredential: () => void;
  onSessionChange: (session: SessionState) => void;
  onViewChange: (view: ViewKey) => void;
  session: SessionState;
  title: string;
}

export function ConsoleLayout({
  activeView,
  apiBaseUrl,
  children,
  isCompact,
  onClearCredential,
  onSessionChange,
  onViewChange,
  session,
  title
}: ConsoleLayoutProps) {
  const [collapsed, setCollapsed] = useState(false);
  const authenticated = Boolean(session.credential.trim());
  const siderWidth = isCompact ? 0 : 264;

  return (
    <Layout className="console-shell">
      {!isCompact && (
        <Sider width={siderWidth} collapsed={collapsed} collapsible trigger={null} className="console-sider">
          <div className="console-brand">
            <DatabaseOutlined />
            {!collapsed && (
              <div>
                <strong>httpkvdb</strong>
                <span>Console</span>
              </div>
            )}
          </div>
          <Menu
            theme="dark"
            mode="inline"
            selectedKeys={[activeView]}
            onClick={({ key }) => onViewChange(key as ViewKey)}
            items={[
              { key: "admin", icon: <DashboardOutlined />, label: "管理面板" },
              { key: "kv", icon: <KeyOutlined />, label: "用户 KV" },
              { key: "tx", icon: <PlayCircleOutlined />, label: "事务工作台" },
              { key: "import", icon: <CloudUploadOutlined />, label: "导入导出" }
            ]}
          />
          <div className="console-session-summary">
            {!collapsed && (
              <>
                <Text type="secondary">后端</Text>
                <Text className="summary-value">{apiBaseUrl}</Text>
                <Text type="secondary">认证</Text>
                <Tag color={authenticated ? "green" : "default"}>{authenticated ? session.authMode : "未配置"}</Tag>
              </>
            )}
            <Button block icon={<LogoutOutlined />} onClick={onClearCredential}>
              {!collapsed && "清除凭据"}
            </Button>
          </div>
        </Sider>
      )}
      <Layout>
        <Header className="console-header">
          <Space align="center" size={14}>
            {!isCompact && (
              <Button
                aria-label="toggle navigation"
                icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
                onClick={() => setCollapsed((value) => !value)}
              />
            )}
            <div>
              <Text type="secondary" className="console-kicker">
                Single-node Serializable KV
              </Text>
              <Title level={2}>{title}</Title>
            </div>
          </Space>
          <SessionPanel session={session} onChange={onSessionChange} />
        </Header>
        {isCompact && (
          <Menu
            className="mobile-nav"
            mode="horizontal"
            selectedKeys={[activeView]}
            onClick={({ key }) => onViewChange(key as ViewKey)}
            items={[
              { key: "admin", icon: <DashboardOutlined />, label: "管理" },
              { key: "kv", icon: <KeyOutlined />, label: "KV" },
              { key: "tx", icon: <PlayCircleOutlined />, label: "事务" },
              { key: "import", icon: <CloudUploadOutlined />, label: "导入" }
            ]}
          />
        )}
        <Content className="console-content">{children}</Content>
      </Layout>
    </Layout>
  );
}
