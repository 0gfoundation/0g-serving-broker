import React, { useState, useEffect } from "react";
import ZGTable from "@src/components/table/table";
import { ModelRefund } from "@src/apis";
import { alertError } from "@src/utils";
import { listRefund } from "@src/apis/operations/refund.api";
import dayjs from "dayjs";
import { CheckOutlined, CloseOutlined } from "@ant-design/icons";

const RefundOverview: React.FC = () => {
  const [loading, setLoading] = useState(false);
  const [dataSource, setDataSource] = useState<ModelRefund[]>([]);

  useEffect(() => {
    setLoading(true);
    listRefund().then((data) => {
      setLoading(false);
      const refunds = data.items || [];
      setDataSource(refunds);
    }, alertError);
  }, []);

  return (
    <ZGTable
      columns={[
        {
          title: "Create Time",
          align: "center",
          dataIndex: "CreatedAt",
          width: "50%",
          render: (date: string) => dayjs(date).format("YYYY-MM-DD HH:mm"),
        },
        {
          title: "Amount",
          align: "center",
          width: "25%",
          dataIndex: "amount",
        },
        {
          title: "Processed",
          align: "center",
          width: "25%",
          dataIndex: "processed",
          render: (processed: boolean) => {
            if (processed) {
              return <CheckOutlined />;
            }
            return <CloseOutlined />;
          },
        },
      ]}
      dataSource={dataSource}
      rowKey="createdAt"
      scroll={{ x: 880 }}
      pagination={{ total: 5 }}
      loading={loading}
    />
  );
};

export default RefundOverview;
