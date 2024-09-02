import React, { useEffect, useState } from "react";
import { Modal, Form, Input, Button, message, InputNumber } from "antd";
import { alertError } from "@src/utils";
import { addProviderAccount, ModelProvider } from "@src/apis";
import { OverlayOpenArgs } from "@src/hooks";

export interface AddAccountModalOpenArgs {
  data?: ModelProvider;
}

export interface AddAccountModalProps {
  openArgs: OverlayOpenArgs<AddAccountModalOpenArgs>;
  onOk: () => void;
  onCancel: () => void;
}

const AddAccountModal: React.FC<AddAccountModalProps> = ({
  openArgs,
  onOk,
  onCancel,
}) => {
  const [form] = Form.useForm();

  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (openArgs.open) {
      form.setFieldsValue({});
    }
  }, [openArgs]);

  const handleError = (error: any) => {
    setLoading(false);
    alertError(error);
  };

  const handleOk = () => {
    form.validateFields().then((values: ModelProvider) => {
      setLoading(true);
      const request = addProviderAccount({
        ...values,
      });
      request.then(() => {
        setLoading(false);
        message.success("Account Created");
        onOk();
        form.resetFields();
      }, handleError);
    }, handleError);
  };

  const handleCancel = () => {
    form.resetFields();
    onCancel();
  };

  return (
    <Modal
      title="Add Account"
      open={openArgs.open}
      onOk={handleOk}
      onCancel={handleCancel}
      loading={loading}
    >
      <Form form={form} layout="vertical">
        <Form.Item
          label="Provider Address"
          name="provider"
          rules={[
            { required: true, message: "Please input the provider address" },
          ]}
        >
          <Input />
        </Form.Item>
        <Form.Item
          label="Balance (neuron)"
          name="balance"
          rules={[{ required: true, message: "Please input the balance" }]}
        >
          <InputNumber style={{ width: "100%" }} />
        </Form.Item>
      </Form>
    </Modal>
  );
};

export default AddAccountModal;
