import { useState } from "react";
import { FileDown, FileUp } from "lucide-react";
import { Button, Card, Col, Form, Row, Select, Upload } from "antd";
import { UploadOutlined } from "@ant-design/icons";
import { useActionRunner } from "../../components/ActionResult";
import { ApiClient, downloadBlob } from "../../lib/api";
import { ImportResult } from "../../lib/types";

export function ImportExportPanel({ client }: { client: ApiClient }) {
  const runAction = useActionRunner();
  const [mode, setMode] = useState<"replace" | "merge-overwrite" | "merge-skip">("replace");
  const [file, setFile] = useState<File | null>(null);
  const [result, setResult] = useState<ImportResult | null>(null);

  async function exportData() {
    await runAction("导出完成", async () => {
      const blob = await client.exportData();
      downloadBlob(blob, `kv-export-${Date.now()}.bin`);
    });
  }

  async function importData() {
    if (!file) {
      throw new Error("请选择导入文件");
    }
    await runAction("导入完成", async () => setResult(await client.importData(file, mode)));
  }

  return (
    <Row gutter={[16, 16]}>
      <Col xs={24} lg={12}>
        <Card title="导出" extra={<FileDown size={20} />}>
          <Button type="primary" icon={<FileDown size={16} />} onClick={exportData}>
            下载导出文件
          </Button>
        </Card>
      </Col>
      <Col xs={24} lg={12}>
        <Card title="导入" extra={<FileUp size={20} />}>
          <Form layout="vertical">
            <Form.Item label="模式">
              <Select
                value={mode}
                onChange={setMode}
                options={[
                  { value: "replace", label: "replace" },
                  { value: "merge-overwrite", label: "merge-overwrite" },
                  { value: "merge-skip", label: "merge-skip" }
                ]}
              />
            </Form.Item>
            <Form.Item label="文件">
              <Upload
                accept="application/octet-stream,.bin"
                beforeUpload={(nextFile) => {
                  setFile(nextFile);
                  return false;
                }}
                maxCount={1}
              >
                <Button icon={<UploadOutlined />}>选择文件</Button>
              </Upload>
            </Form.Item>
          </Form>
          <Button type="primary" icon={<FileUp size={16} />} onClick={importData}>
            上传并导入
          </Button>
          <pre className="console-output">{result ? JSON.stringify(result, null, 2) : "尚未导入"}</pre>
        </Card>
      </Col>
    </Row>
  );
}
