import { useEffect, useState } from "react";
import { Activity, Copy, KeyRound, RefreshCw, Trash2, UserPlus } from "lucide-react";
import { App, Button, Card, Col, Descriptions, Form, Input, Modal, Row, Select, Space, Statistic, Table, Tabs, Typography } from "antd";
import { MetadataDescriptions } from "../../components/MetadataDescriptions";
import { useActionRunner } from "../../components/ActionResult";
import { ApiClient, decodeText } from "../../lib/api";
import { AdminKeyInfo, CreatedUserspace, KvMetadata, UserspaceInfo } from "../../lib/types";

const { TextArea } = Input;
const { Text } = Typography;
const contentTypes = ["application/json", "text/plain", "application/octet-stream"];

interface AdminPanelProps {
  apiBaseUrl: string;
  client: ApiClient;
}

export function AdminPanel({ apiBaseUrl, client }: AdminPanelProps) {
  const { message } = App.useApp();
  const runAction = useActionRunner();
  const [health, setHealth] = useState("未检查");
  const [ready, setReady] = useState("未检查");
  const [metrics, setMetrics] = useState<Record<string, string>>({});
  const [userspaces, setUserspaces] = useState<UserspaceInfo[]>([]);
  const [selectedUserspace, setSelectedUserspace] = useState("");
  const [created, setCreated] = useState<CreatedUserspace | null>(null);
  const [keys, setKeys] = useState<AdminKeyInfo[]>([]);
  const [kvKey, setKvKey] = useState("profile");
  const [contentType, setContentType] = useState("application/json");
  const [value, setValue] = useState('{"name":"Alice"}');
  const [metadata, setMetadata] = useState<KvMetadata>({});
  const [readValue, setReadValue] = useState("");
  const [form] = Form.useForm<{ userspaceId: string; userId: string }>();

  useEffect(() => {
    void refreshUserspaces();
  }, []);

  async function refreshStatus() {
    await runAction("服务状态已刷新", async () => {
      const [healthText, readyText, metricsText] = await Promise.all([client.health("/healthz"), client.health("/readyz"), client.health("/metrics")]);
      setHealth(healthText.trim() || "ok");
      setReady(readyText.trim() || "ok");
      setMetrics(parseMetrics(metricsText));
    });
  }

  async function refreshUserspaces() {
    await runAction("用户空间已刷新", async () => {
      const list = await client.listUserspaces();
      setUserspaces(list);
      setSelectedUserspace((current) => current || list[0]?.userspace_id || "");
    });
  }

  async function createUserspace(values: { userspaceId: string; userId: string }) {
    await runAction("用户空间已创建", async () => {
      const result = await client.createUserspace(values.userspaceId.trim(), values.userId.trim());
      setCreated(result);
      setSelectedUserspace(result.userspace_id);
      await refreshUserspaces();
    });
  }

  async function rotateAPIKey(userspaceId: string) {
    await runAction("APIKey 已轮换", async () => {
      const result = await client.rotateUserspaceAPIKey(userspaceId);
      setCreated(result);
      await refreshUserspaces();
    });
  }

  async function deleteUserspace(userspaceId: string) {
    Modal.confirm({
      title: `删除 userspace ${userspaceId}`,
      content: "该操作会删除该 userspace 的 KV 数据、APIKey、JWT 映射和事务状态。",
      okText: "删除",
      okButtonProps: { danger: true },
      onOk: async () => {
        await runAction("用户空间已删除", async () => {
          await client.deleteUserspace(userspaceId);
          setSelectedUserspace("");
          setKeys([]);
          await refreshUserspaces();
        });
      }
    });
  }

  async function copyAPIKey() {
    if (!created?.api_key) {
      return;
    }
    await navigator.clipboard.writeText(created.api_key);
    message.success("APIKey 已复制");
  }

  async function refreshKeys() {
    if (!selectedUserspace) {
      message.warning("请选择 userspace");
      return;
    }
    await runAction("Key 列表已刷新", async () => setKeys(await client.listAdminKeys(selectedUserspace)));
  }

  async function putKey() {
    await requireUserspace(async (userspace) => {
      await runAction("写入成功", async () => {
        setMetadata(await client.putAdminKey(userspace, kvKey, value, contentType));
        await refreshKeys();
      });
    });
  }

  async function getKey() {
    await requireUserspace(async (userspace) => {
      await runAction("读取成功", async () => {
        const result = await client.getAdminKey(userspace, kvKey);
        setMetadata(result.metadata);
        setReadValue(decodeText(result.value, result.metadata.contentType));
      });
    });
  }

  async function headKey() {
    await requireUserspace(async (userspace) => {
      await runAction("元信息已刷新", async () => setMetadata(await client.headAdminKey(userspace, kvKey)));
    });
  }

  async function deleteKey() {
    await requireUserspace(async (userspace) => {
      await runAction("删除成功", async () => {
        await client.deleteAdminKey(userspace, kvKey);
        setMetadata({});
        setReadValue("");
        await refreshKeys();
      });
    });
  }

  async function requireUserspace(action: (userspace: string) => Promise<void>) {
    if (!selectedUserspace) {
      message.warning("请选择 userspace");
      return;
    }
    await action(selectedUserspace);
  }

  const metricRows = ["kv_get_total", "kv_put_total", "kv_delete_total", "tx_created_total", "tx_committed_total", "tx_aborted_total", "import_total", "export_total"].map(
    (name) => ({ key: name, name, value: metrics[name] || "0" })
  );

  return (
    <Tabs
      items={[
        {
          key: "userspaces",
          label: "用户空间",
          children: (
            <Row gutter={[16, 16]}>
              <Col xs={24} xl={9}>
                <Card title="创建 userspace" extra={<UserPlus size={20} />}>
                  <Form form={form} layout="vertical" initialValues={{ userspaceId: "webapp", userId: "webapp" }} onFinish={createUserspace}>
                    <Form.Item label="Userspace ID" name="userspaceId" rules={[{ required: true, message: "请输入 userspace" }]}>
                      <Input />
                    </Form.Item>
                    <Form.Item label="User ID" name="userId">
                      <Input />
                    </Form.Item>
                    <Button type="primary" htmlType="submit" icon={<UserPlus size={16} />}>
                      创建
                    </Button>
                  </Form>
                  {created && (
                    <div className="created-credential">
                      <Descriptions bordered size="small" column={1}>
                        <Descriptions.Item label="userspace">{created.userspace_id}</Descriptions.Item>
                        <Descriptions.Item label="user">{created.user_id}</Descriptions.Item>
                      </Descriptions>
                      <Text type="secondary">APIKey</Text>
                      <TextArea readOnly value={created.api_key} rows={3} />
                      <Button icon={<Copy size={16} />} onClick={copyAPIKey}>
                        复制 APIKey
                      </Button>
                    </div>
                  )}
                </Card>
              </Col>
              <Col xs={24} xl={15}>
                <Card
                  title="userspace 管理"
                  extra={
                    <Button icon={<RefreshCw size={16} />} onClick={refreshUserspaces}>
                      刷新
                    </Button>
                  }
                >
                  <Table
                    rowKey="userspace_id"
                    dataSource={userspaces}
                    pagination={false}
                    size="small"
                    rowSelection={{
                      type: "radio",
                      selectedRowKeys: selectedUserspace ? [selectedUserspace] : [],
                      onChange: ([key]) => setSelectedUserspace(String(key || ""))
                    }}
                    columns={[
                      { title: "Userspace", dataIndex: "userspace_id" },
                      { title: "User", dataIndex: "user_id" },
                      { title: "Keys", dataIndex: "key_count", width: 90 },
                      { title: "APIKeys", dataIndex: "api_key_count", width: 100 },
                      {
                        title: "操作",
                        width: 220,
                        render: (_, row) => (
                          <Space wrap>
                            <Button size="small" icon={<KeyRound size={14} />} onClick={() => rotateAPIKey(row.userspace_id)}>
                              轮换
                            </Button>
                            <Button size="small" danger icon={<Trash2 size={14} />} onClick={() => deleteUserspace(row.userspace_id)}>
                              删除
                            </Button>
                          </Space>
                        )
                      }
                    ]}
                  />
                </Card>
              </Col>
            </Row>
          )
        },
        {
          key: "kv",
          label: "数据颗粒管理",
          children: (
            <Row gutter={[16, 16]}>
              <Col xs={24} xl={10}>
                <Card
                  title="Key 列表"
                  extra={
                    <Space>
                      <Select
                        className="admin-userspace-select"
                        placeholder="userspace"
                        value={selectedUserspace || undefined}
                        onChange={setSelectedUserspace}
                        options={userspaces.map((item) => ({ value: item.userspace_id, label: item.userspace_id }))}
                      />
                      <Button icon={<RefreshCw size={16} />} onClick={refreshKeys}>
                        刷新
                      </Button>
                    </Space>
                  }
                >
                  <Table
                    rowKey="key"
                    dataSource={keys}
                    pagination={{ pageSize: 8 }}
                    size="small"
                    onRow={(row) => ({ onClick: () => setKvKey(row.key) })}
                    columns={[
                      { title: "Key", dataIndex: "key" },
                      { title: "Type", dataIndex: "value_type", width: 90 },
                      { title: "Version", dataIndex: "version", width: 90 }
                    ]}
                  />
                </Card>
              </Col>
              <Col xs={24} xl={14}>
                <Card title="KV CRUD" extra={<KeyRound size={20} />}>
                  <Form layout="vertical">
                    <Form.Item label="Key">
                      <Input value={kvKey} onChange={(event) => setKvKey(event.target.value)} />
                    </Form.Item>
                    <Form.Item label="Content-Type">
                      <Select value={contentType} onChange={setContentType} options={contentTypes.map((item) => ({ value: item, label: item }))} />
                    </Form.Item>
                    <Form.Item label="Value">
                      <TextArea value={value} onChange={(event) => setValue(event.target.value)} rows={8} />
                    </Form.Item>
                  </Form>
                  <Space wrap>
                    <Button type="primary" onClick={putKey}>
                      PUT
                    </Button>
                    <Button onClick={getKey}>GET</Button>
                    <Button onClick={headKey}>HEAD</Button>
                    <Button danger onClick={deleteKey}>
                      DELETE
                    </Button>
                  </Space>
                  <MetadataDescriptions metadata={metadata} />
                  <pre className="console-output">{readValue || "尚未读取 value"}</pre>
                </Card>
              </Col>
            </Row>
          )
        },
        {
          key: "status",
          label: "状态指标",
          children: (
            <Row gutter={[16, 16]}>
              <Col xs={24} xl={12}>
                <Card
                  title="服务状态"
                  extra={
                    <Button icon={<RefreshCw size={16} />} onClick={refreshStatus}>
                      刷新
                    </Button>
                  }
                >
                  <Row gutter={[12, 12]}>
                    <Col xs={24} sm={12}>
                      <Statistic title="API Base URL" value={apiBaseUrl} valueStyle={{ fontSize: 16 }} />
                    </Col>
                    <Col xs={12} sm={6}>
                      <Statistic title="healthz" value={health} valueStyle={{ color: health === "ok" ? "#17803d" : undefined }} />
                    </Col>
                    <Col xs={12} sm={6}>
                      <Statistic title="readyz" value={ready} valueStyle={{ color: ready === "ok" ? "#17803d" : undefined }} />
                    </Col>
                  </Row>
                </Card>
              </Col>
              <Col xs={24} xl={12}>
                <Card title="核心指标" extra={<Activity size={20} />}>
                  <Table
                    columns={[
                      { title: "Metric", dataIndex: "name" },
                      { title: "Value", dataIndex: "value", width: 160, align: "right" }
                    ]}
                    dataSource={metricRows}
                    pagination={false}
                    size="small"
                  />
                </Card>
              </Col>
            </Row>
          )
        }
      ]}
    />
  );
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
