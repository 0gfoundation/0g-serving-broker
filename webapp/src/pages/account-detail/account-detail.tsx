import React, { useState } from "react";
import { Empty, Tabs, TabsProps } from "antd";
import { ModelProvider } from "@src/apis";
import styles from "./account-detail.module.css";
import {
  InteractionTwoTone,
  MinusCircleTwoTone,
  PlusCircleTwoTone,
  RocketTwoTone,
} from "@ant-design/icons";

export interface AccountDetailProps {
  selectedAccount: ModelProvider | null;
}

const AccountDetail: React.FC<AccountDetailProps> = ({ selectedAccount }) => {
  const items: TabsProps["items"] = [
    {
      key: "1",
      label: "Request Record",
      children: "Content of Request Record",
    },
    {
      key: "2",
      label: "Refund Record",
      children: "Content of Refund Record",
    },
  ];

  const handleTabChange = (item: string) => {
    console.log(item);
  };

  const ZGAccount: React.FC<{
    account: ModelProvider;
    onChange: (item: string) => void;
  }> = ({ account, onChange }) => (
    <div style={{ width: "100%" }}>
      <p style={{ fontSize: 30, width: "100%", margin: 0 }}>
        {((account.balance || 0) / 10 ** 18).toPrecision(2)} A0GI
      </p>

      <div style={{ display: "flex", justifyContent: "center", width: "100%" }}>
        <div
          style={{
            display: "inline-block",
            lineHeight: "normal",
            cursor: "pointer",
            width: "70px",
          }}
        >
          <PlusCircleTwoTone
            style={{ fontSize: 30, verticalAlign: "middle" }}
          />
          <p
            style={{
              margin: 0,
              padding: "5px",
              verticalAlign: "middle",
              lineHeight: "normal",
              color: "grey",
              fontSize: "12px",
            }}
          >
            Charge
          </p>
        </div>
        <div
          style={{
            display: "inline-block",
            lineHeight: "normal",
            cursor: "pointer",
            width: "70px",
          }}
        >
          <MinusCircleTwoTone
            style={{ fontSize: 30, verticalAlign: "middle" }}
          />
          <p
            style={{
              margin: 0,
              padding: "5px",
              verticalAlign: "middle",
              lineHeight: "normal",
              color: "grey",
              fontSize: "12px",
            }}
          >
            Refund
          </p>
        </div>

        <div
          style={{
            display: "inline-block",
            lineHeight: "normal",
            cursor: "pointer",
            width: "70px",
          }}
        >
          <InteractionTwoTone
            style={{ fontSize: 30, verticalAlign: "middle" }}
          />
          <p
            style={{
              margin: 0,
              padding: "5px",
              verticalAlign: "middle",
              lineHeight: "normal",
              color: "grey",
              fontSize: "12px",
            }}
          >
            Sync
          </p>
        </div>

        <div
          style={{
            display: "inline-block",
            lineHeight: "normal",
            cursor: "pointer",
            width: "70px",
            marginBottom: "20px",
          }}
        >
          <RocketTwoTone style={{ fontSize: 30, verticalAlign: "middle" }} />
          <p
            style={{
              margin: 0,
              padding: "5px",
              verticalAlign: "middle",
              lineHeight: "normal",
              color: "grey",
              fontSize: "12px",
            }}
          >
            Service
          </p>
        </div>
      </div>

      <div style={{ display: "flex", justifyContent: "center", width: "100%" }}>
        <Tabs
          size="large"
          defaultActiveKey="1"
          items={items}
          onChange={onChange}
        />
      </div>
    </div>
  );

  return (
    <div style={{ height: "100%", display: "flex", justifyContent: "center" }}>
      {selectedAccount ? (
        <ZGAccount account={selectedAccount} onChange={handleTabChange} />
      ) : (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
      )}
    </div>
  );
};

export default AccountDetail;
