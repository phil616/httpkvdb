import { useState } from "react";
import { Activity, KeyRound, Plus, RefreshCw, Trash2 } from "lucide-react";
import { Button, Card, Col, Form, Input, Row, Select, Space } from "antd";
import { MetadataDescriptions } from "../../components/MetadataDescriptions";
import { useActionRunner } from "../../components/ActionResult";
import { ApiClient, decodeText } from "../../lib/api";
import { KvMetadata } from "../../lib/types";

const { TextArea } = Input;
const contentTypes = ["application/json", "text/plain", "application/octet-stream"];

export function KvPanel({ client }: { client: ApiClient }) {
  const runAction = useActionRunner();
  const [userspace, setUserspace] = useState("");
  const [key, setKey] = useState("profile");
  const [contentType, setContentType] = useState("application/json");
  const [value, setValue] = useState('{"name":"Alice"}');
  const [metadata, setMetadata] = useState<KvMetadata>({});
  const [readValue, setReadValue] = useState("");

  async function put() {
    await runAction("写入成功", async () => setMetadata(await client.putKey(key, value, contentType, userspace)));
  }

  async function get() {
    await runAction("读取成功", async () => {
      const result = await client.getKey(key, userspace);
      setMetadata(result.metadata);
      setReadValue(decodeText(result.value, result.metadata.contentType));
    });
  }

  async function head() {
    await runAction("元信息已刷新", async () => setMetadata(await client.headKey(key, userspace)));
  }

  async function remove() {
    await runAction("删除成功", async () => {
      await client.deleteKey(key, userspace);
      setMetadata({});
      setReadValue("");
    });
  }

  return (
    <Row gutter={[16, 16]}>
      <Col xs={24} xl={14}>
        <Card title="键值操作" extra={<KeyRound size={20} />}>
          <Form layout="vertical">
            <Form.Item label="Userspace">
              <Input value={userspace} onChange={(event) => setUserspace(event.target.value)} placeholder="alice" />
            </Form.Item>
            <Form.Item label="Key" required>
              <Input value={key} onChange={(event) => setKey(event.target.value)} />
            </Form.Item>
            <Form.Item label="Content-Type">
              <Select value={contentType} onChange={setContentType} options={contentTypes.map((item) => ({ value: item, label: item }))} />
            </Form.Item>
            <Form.Item label="Value">
              <TextArea value={value} onChange={(event) => setValue(event.target.value)} rows={10} />
            </Form.Item>
          </Form>
          <Space wrap>
            <Button type="primary" icon={<Plus size={16} />} onClick={put}>
              PUT
            </Button>
            <Button icon={<RefreshCw size={16} />} onClick={get}>
              GET
            </Button>
            <Button icon={<Activity size={16} />} onClick={head}>
              HEAD
            </Button>
            <Button danger icon={<Trash2 size={16} />} onClick={remove}>
              DELETE
            </Button>
          </Space>
        </Card>
      </Col>
      <Col xs={24} xl={10}>
        <Card title="读取结果">
          <MetadataDescriptions metadata={metadata} />
          <pre className="console-output">{readValue || "尚未读取 value"}</pre>
        </Card>
      </Col>
    </Row>
  );
}
