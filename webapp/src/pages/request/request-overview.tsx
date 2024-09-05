import React, { useState, useEffect } from "react";
import ZGTable from "@src/components/table/table";
import { ModelRequest } from "@src/apis";
import { alertError } from "@src/utils";
import { listRequest } from "@src/apis/operations/request.api";

const RequestOverview: React.FC = () => {
  const [loading, setLoading] = useState(false);
  const [dataSource, setDataSource] = useState<ModelRequest[]>([]);

  useEffect(() => {
    setLoading(true);
    listRequest().then((data) => {
      setLoading(false);
      const requests = data.items || [];
      setDataSource(requests);
    }, alertError);
  }, []);

  return (
    <ZGTable
      columns={[
        {
          title: "Service",
          align: "center",
          dataIndex: "serviceName",
          width: "20%",
        },
        {
          title: "Input",
          align: "center",
          dataIndex: "inputCount",
          width: "20%",
        },
        {
          title: "Previous Output Fee",
          align: "center",
          dataIndex: "previousOutputFee",
          width: "50%",
        },
        {
          title: "Fee",
          align: "center",
          width: "10%",
          dataIndex: "fee",
        },
      ]}
      dataSource={dataSource}
      rowKey="nonce"
      scroll={{ x: 880 }}
      pagination={{ total: 5 }}
      loading={loading}
    />
  );
};

export default RequestOverview;
