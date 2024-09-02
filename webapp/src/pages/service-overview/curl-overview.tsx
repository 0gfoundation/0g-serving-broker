import React, { useState, useEffect } from "react";
import { Modal } from "antd";
import { ModelService } from "@src/apis";
import { OverlayOpenArgs } from "@src/hooks";
import { ProDescriptions } from "@ant-design/pro-components";

export interface CurlModalArgs {
  data?: ModelService;
}

export interface CurlModalProps {
  openArgs: OverlayOpenArgs<CurlModalArgs>;
  onCancel: () => void;
}

const CurlOverview: React.FC<CurlModalProps> = ({ openArgs, onCancel }) => {
  const [curlContent, setContent] = useState<string>("");

  useEffect(() => {
    if (openArgs.open) {
      const content = `curl -N http://192.168.2.142:1034/v1/provider/${openArgs.args.data?.provider}/service/${openArgs.args.data?.name}/chat/completions \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $OPENAI_API_KEY" \
    -H "Accept-Encoding: [gzip, deflate, br]" \
    -d '{
       "model": "gpt-3.5-turbo",
       "messages": [{"role": "user", "content": "Say this is a test!"}],
       "temperature": 0.7
     }'    
    `;
      setContent(content);
    }
  }, [openArgs]);

  const handleCancel = () => {
    onCancel();
  };

  return (
    <Modal
      title="Curl Command"
      open={openArgs.open}
      footer={null}
      onCancel={handleCancel}
    >
      <ProDescriptions>
        <ProDescriptions.Item copyable={true}>
          {curlContent}
        </ProDescriptions.Item>
      </ProDescriptions>
    </Modal>
  );
};

export default CurlOverview;
