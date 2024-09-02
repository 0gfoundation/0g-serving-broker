import React, { useState, useEffect } from "react";
import { Button, Modal, Empty } from "antd";
import { listService } from "@src/apis/operations";
import { ModelService, ModelServiceList } from "@src/apis";
import { OverlayOpenArgs, useOverlay } from "@src/hooks";
import { ProDescriptions } from "@ant-design/pro-components";
import { alertError } from "@src/utils";
import dayjs from "dayjs";
import CurlOverview, { CurlModalArgs } from "./curl-overview";

export interface ServiceModalProps {
  openArgs: OverlayOpenArgs<any>;
  onCancel: () => void;
}

const ServiceOverview: React.FC<ServiceModalProps> = ({
  openArgs,
  onCancel,
}) => {
  const [loading, setLoading] = useState(false);
  const [providerServices, setProviderServices] = useState<ModelService[]>([]);

  const {
    openArgs: curlModalOpenArgs,
    setOpenArgs: setCurlModalOpenArgs,
    setOpen: setCurlModalOpen,
  } = useOverlay<CurlModalArgs>();

  useEffect(() => {
    if (openArgs.open) {
      setLoading(true);
      listService().then((data: ModelServiceList) => {
        setLoading(false);
        const services = data.items || [];
        setProviderServices(services);
      }, alertError);
    }
  }, [openArgs]);

  const handleCancel = () => {
    onCancel();
  };

  const handleServiceClick = (service: ModelService) => {
    setCurlModalOpenArgs({ open: true, args: { data: service } });
  };

  if (loading) {
    return <></>;
  }

  const ServiceItem: React.FC<{
    service: ModelService;
    onClick: () => void;
  }> = ({ service, onClick }) => {
    const tooltip = `The price of each unit(e.g. token for chatbot service) in request: input(${service.inputPrice} neuron); output(${service.outputPrice} (neuron))`;
    const updatedAt = dayjs(service.updatedAt).format("YYYY-MM-DD HH:mm");

    return (
      <>
        <div
          key={service.provider}
          style={{
            display: "flex",
            flexDirection: "column",
            marginBottom: "30px",
          }}
        >
          <ProDescriptions
            columns={[
              {
                title: "option",
                valueType: "option",
                render: () => [<a onClick={onClick}>curl command</a>],
              },
            ]}
            title={<Title label={service.name || ""} />}
            tooltip={tooltip}
            colon={false}
            size="small"
          >
            <ProDescriptions.Item label="Updated Time">
              {updatedAt}
            </ProDescriptions.Item>
            <ProDescriptions.Item
              label="Provider"
              ellipsis={true}
              copyable={true}
            >
              {service.provider}
            </ProDescriptions.Item>
          </ProDescriptions>
        </div>

        <CurlOverview
          openArgs={curlModalOpenArgs}
          onCancel={() => setCurlModalOpen(false)}
        ></CurlOverview>
      </>
    );
  };

  const Title: React.FC<{ label: string }> = ({ label }) => {
    return <span style={{ fontWeight: "bold" }}>{label}</span>;
  };

  return (
    <Modal
      title="Services"
      open={openArgs.open}
      onCancel={handleCancel}
      footer={null}
      loading={loading}
    >
      <div>
        {providerServices.length > 0 ? (
          providerServices.map((service) => {
            return (
              <ServiceItem
                key={service.provider}
                service={service}
                onClick={() => {
                  handleServiceClick(service);
                }}
              />
            );
          })
        ) : (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
        )}
      </div>
    </Modal>
  );
};

export default ServiceOverview;
