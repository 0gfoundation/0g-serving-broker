import React, { FC, ReactElement, useState } from "react";
import { Layout } from "antd";
import AccountOverview from "../account-overview/account-overview";
import styles from "./layout.module.css";
import { ModelProvider } from "@src/apis";
import AccountDetail from "../account-detail/account-detail";

const { Header, Footer, Content } = Layout;

const AgLayout: React.FC = () => {
  const [selectedAccount, setSelectedAccount] = useState<ModelProvider | null>(
    null
  );

  const handleSelectAccount = (account: ModelProvider) => {
    setSelectedAccount(account);
  };

  return (
    <Layout>
      <Header className={styles.header}>
        <AccountOverview onSelectAccount={handleSelectAccount} />
      </Header>
      <Content className={styles.content}>
        <AccountDetail selectedAccount={selectedAccount} />
      </Content>
    </Layout>
  );
};

export default AgLayout;
