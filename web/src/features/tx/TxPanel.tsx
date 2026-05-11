import { useState } from "react";
import { Lock, Play, Plus, RefreshCw, Trash2 } from "lucide-react";
import { Button, Card, Col, Divider, Form, Input, InputNumber, Row, Select, Space } from "antd";
import { useActionRunner } from "../../components/ActionResult";
import { ApiClient } from "../../lib/api";
import { TxDraftOp, TxResult, TxStatus } from "../../lib/types";

const { TextArea } = Input;
const contentTypes = ["application/json", "text/plain", "application/octet-stream"];

export function TxPanel({ client }: { client: ApiClient }) {
  const runAction = useActionRunner();
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
    <Row gutter={[16, 16]}>
      <Col xs={24} xl={15}>
        <Card title="事务控制" extra={<Lock size={20} />}>
          <Row gutter={12}>
            <Col xs={24} md={12}>
              <Form.Item label="Tx ID">
                <Input value={txId} onChange={(event) => setTxId(event.target.value)} />
              </Form.Item>
            </Col>
            <Col xs={12} md={6}>
              <Form.Item label="Total Ops">
                <InputNumber min={1} value={totalOps} onChange={(value) => setTotalOps(Number(value || 1))} className="full-width" />
              </Form.Item>
            </Col>
            <Col xs={12} md={6}>
              <Form.Item label="Timeout ms">
                <InputNumber min={1} value={timeoutMs} onChange={(value) => setTimeoutMs(Number(value || 1))} className="full-width" />
              </Form.Item>
            </Col>
            <Col xs={24}>
              <Form.Item label="Tx Digest">
                <Input value={digest} onChange={(event) => setDigest(event.target.value)} placeholder="可选" />
              </Form.Item>
            </Col>
          </Row>
          <Space wrap>
            <Button type="primary" icon={<Plus size={16} />} onClick={() => runAction("事务已创建", async () => setResult(await client.createTx(txId, totalOps, timeoutMs)))}>
              创建
            </Button>
            <Button icon={<Play size={16} />} onClick={() => runAction("事务已提交", async () => setResult(await client.commitTx(txId, totalOps, digest)))}>
              Commit
            </Button>
            <Button icon={<RefreshCw size={16} />} onClick={() => runAction("结果已刷新", async () => setResult(await client.getTxResult(txId)))}>
              Result
            </Button>
            <Button danger icon={<Trash2 size={16} />} onClick={() => runAction("事务已中止", async () => setResult(await client.abortTx(txId)))}>
              Abort
            </Button>
          </Space>
          <Divider />
          <Row gutter={12}>
            <Col xs={12} md={6}>
              <Form.Item label="Seq">
                <InputNumber min={1} value={op.seq} onChange={(value) => updateOp("seq", Number(value || 1))} className="full-width" />
              </Form.Item>
            </Col>
            <Col xs={12} md={6}>
              <Form.Item label="Op">
                <Select value={op.op} onChange={(value) => updateOp("op", value)} options={["PUT", "GET", "DELETE", "EXISTS", "HEAD"].map((item) => ({ value: item, label: item }))} />
              </Form.Item>
            </Col>
            <Col xs={24} md={12}>
              <Form.Item label="Key">
                <Input value={op.key} onChange={(event) => updateOp("key", event.target.value)} />
              </Form.Item>
            </Col>
            <Col xs={24} md={12}>
              <Form.Item label="Op ID">
                <Input value={op.opId} onChange={(event) => updateOp("opId", event.target.value)} />
              </Form.Item>
            </Col>
            <Col xs={24} md={12}>
              <Form.Item label="Content-Type">
                <Select value={op.contentType} onChange={(value) => updateOp("contentType", value)} options={contentTypes.map((item) => ({ value: item, label: item }))} />
              </Form.Item>
            </Col>
            <Col xs={24}>
              <Form.Item label="Body">
                <TextArea disabled={op.op !== "PUT"} value={op.body} onChange={(event) => updateOp("body", event.target.value)} rows={6} />
              </Form.Item>
            </Col>
          </Row>
          <Button
            type="primary"
            icon={<Plus size={16} />}
            onClick={() => runAction("事务片段已提交", async () => setResult(await client.addTxOp(txId, op.seq, op.op, op.key, op.opId, op.contentType, op.body)))}
          >
            Add Op
          </Button>
        </Card>
      </Col>
      <Col xs={24} xl={9}>
        <Card title="执行状态">
          <pre className="console-output">{result ? JSON.stringify(result, null, 2) : "尚无事务结果"}</pre>
        </Card>
      </Col>
    </Row>
  );
}
