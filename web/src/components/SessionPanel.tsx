import { useEffect, useState } from "react";
import { LockOutlined, SafetyCertificateOutlined } from "@ant-design/icons";
import { Button, Checkbox, Form, Input, Select } from "antd";
import { SessionState } from "../lib/types";

interface SessionPanelProps {
  onChange: (session: SessionState) => void;
  session: SessionState;
}

export function SessionPanel({ onChange, session }: SessionPanelProps) {
  const [draft, setDraft] = useState(session);

  useEffect(() => {
    setDraft(session);
  }, [session]);

  return (
    <Form className="session-form" layout="vertical" onFinish={() => onChange(draft)}>
      <Form.Item label="后端地址">
        <Input value={draft.apiBaseUrl} onChange={(event) => setDraft({ ...draft, apiBaseUrl: event.target.value })} />
      </Form.Item>
      <Form.Item label="认证方式">
        <Select
          value={draft.authMode}
          onChange={(authMode) => setDraft({ ...draft, authMode })}
          options={[
            { value: "ApiKey", label: "APIKey" },
            { value: "Bearer", label: "JWT Bearer" }
          ]}
        />
      </Form.Item>
      <Form.Item label="凭据">
        <Input.Password value={draft.credential} onChange={(event) => setDraft({ ...draft, credential: event.target.value })} autoComplete="off" />
      </Form.Item>
      <Form.Item className="session-checkbox">
        <Checkbox checked={draft.rememberCredential} onChange={(event) => setDraft({ ...draft, rememberCredential: event.target.checked })}>
          <LockOutlined /> 保存
        </Checkbox>
      </Form.Item>
      <Form.Item>
        <Button type="primary" htmlType="submit" icon={<SafetyCertificateOutlined />}>
          应用
        </Button>
      </Form.Item>
    </Form>
  );
}
