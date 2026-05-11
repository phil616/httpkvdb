import { Descriptions } from "antd";
import { KvMetadata } from "../lib/types";

export function MetadataDescriptions({ metadata }: { metadata: KvMetadata }) {
  return (
    <Descriptions bordered column={{ xs: 1, sm: 2 }} size="small">
      <Descriptions.Item label="Version">{metadata.version || "-"}</Descriptions.Item>
      <Descriptions.Item label="Size">{metadata.size || "-"}</Descriptions.Item>
      <Descriptions.Item label="Checksum">{metadata.checksum || "-"}</Descriptions.Item>
      <Descriptions.Item label="Content-Type">{metadata.contentType || "-"}</Descriptions.Item>
    </Descriptions>
  );
}
