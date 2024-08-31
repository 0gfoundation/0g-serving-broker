import React from "react";
import { Table } from "antd";
import type { TableProps } from "antd";

const ZGTable: React.FC<TableProps> = ({ columns, dataSource }) => {
  return (
    <Table
      columns={columns}
      dataSource={dataSource}
      pagination={{
        defaultPageSize: 5,
        size: "small",
      }}
    />
  );
};

export default ZGTable;
