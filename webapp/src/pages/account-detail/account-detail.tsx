import React, { useState } from "react";
import { Empty, Spin, Tabs, TabsProps } from "antd";
import {
  charge,
  getProviderAccount,
  ModelProvider,
  syncProviderAccount,
} from "@src/apis";
import styles from "./account-detail.module.css";
import {
  InteractionTwoTone,
  MinusCircleTwoTone,
  PlusCircleTwoTone,
  RocketTwoTone,
} from "@ant-design/icons";
import { alertError } from "@src/utils";
import RequestOverview from "../request/request-overview";
import RefundOverview from "../refund/refund-overview";

export interface AccountDetailProps {
  selectedAccount: ModelProvider | null;
}

const AccountDetail: React.FC<AccountDetailProps> = ({ selectedAccount }) => {
  const [loading, setLoading] = useState(false);

  const items: TabsProps["items"] = [
    {
      key: "1",
      label: "Request Record",
      children: <RequestOverview />,
    },
    {
      key: "2",
      label: "Refund Record",
      children: <RefundOverview />,
    },
  ];

  const handleCharge = () => {
    // setLoading(true);
    // charge().then((data) => {
    //   setLoading(false);
    //   const accounts = data.items || [];
    //   setProviderAccounts(accounts);
    //   if (accounts.length > 0) {
    //     onSelectAccount(accounts[0]);
    //     setProviderAccount(accounts[0]);
    //   }
    // }, alertError);
  };

  const handleRefund = () => {
    console.log("123");
  };

  const handleSync = () => {
    setLoading(true);
    const address = selectedAccount?.provider || "";
    syncProviderAccount(address).then(() => {
      getProviderAccount(address).then((data) => {
        console.log(data.balance);
        setLoading(false);
        selectedAccount = data;
      }, alertError);
    }, alertError);
  };

  const handleListService = () => {
    console.log("123");
  };

  const handleTabChange = (item: string) => {
    console.log(item);
  };

  const ZGAccount: React.FC<{
    account: ModelProvider;
    onChange: (item: string) => void;
  }> = ({ account, onChange }) => (
    <div style={{ width: "100%" }}>
      {!loading ? (
        <p
          style={{
            height: "80px",
            fontSize: 30,
            width: "100%",
            margin: 0,
            justifyContent: "center",
            display: "flex",
            alignItems: "center",
          }}
        >
          {((account.balance || 0) / 10 ** 18).toPrecision(2)} A0GI
        </p>
      ) : (
        <Spin />
      )}
      <div style={{ display: "flex", justifyContent: "center", width: "100%" }}>
        <div onClick={handleCharge} className={styles.operations}>
          <PlusCircleTwoTone
            style={{ fontSize: 30, verticalAlign: "middle" }}
          />
          <p className={styles["operation-name"]}>Charge</p>
        </div>
        <div onClick={handleRefund} className={styles.operations}>
          <MinusCircleTwoTone
            style={{ fontSize: 30, verticalAlign: "middle" }}
          />
          <p className={styles["operation-name"]}>Refund</p>
        </div>
        <div onClick={handleSync} className={styles.operations}>
          <InteractionTwoTone
            style={{ fontSize: 30, verticalAlign: "middle" }}
          />
          <p className={styles["operation-name"]}>Sync</p>
        </div>
        <div onClick={handleListService} className={styles.operations}>
          <RocketTwoTone style={{ fontSize: 30, verticalAlign: "middle" }} />
          <p className={styles["operation-name"]}>Service</p>
        </div>
      </div>
      <div style={{ display: "flex", justifyContent: "center", width: "100%" }}>
        <Tabs
          size="large"
          defaultActiveKey="1"
          items={items}
          onChange={onChange}
          centered={true}
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
