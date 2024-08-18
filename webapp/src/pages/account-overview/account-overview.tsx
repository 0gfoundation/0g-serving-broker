import React, { useState, useEffect } from "react";
import { Button, Modal, Empty, Spin } from "antd";
import { DownOutlined } from "@ant-design/icons";
import { listProviderAccount } from "@src/apis/operations";
import { ModelProvider } from "@src/apis";
import { useOverlay } from "@src/hooks";
import AddAccountModal, { AddAccountModalOpenArgs } from "./account-modal";
import { alertError } from "@src/utils";
import styles from "./account.module.css";

export interface AccountOverviewProps {
  onSelectAccount: (account: ModelProvider) => void;
}

const AccountOverview: React.FC<AccountOverviewProps> = ({
  onSelectAccount,
}) => {
  const [loading, setLoading] = useState(false);
  const [isModalOpen, setIsModalOpen] = useState(false);
  const [providerAccount, setProviderAccount] = useState<ModelProvider>();
  const [providerAccounts, setProviderAccounts] = useState<ModelProvider[]>([]);

  useEffect(() => {
    setLoading(true);
    listProviderAccount().then((data) => {
      setLoading(false);
      const accounts = data.items || [];
      setProviderAccounts(accounts);
      if (accounts.length > 0) {
        onSelectAccount(accounts[0]);
        setProviderAccount(accounts[0]);
      }
    }, alertError);
  }, []);

  const showModal = () => {
    setIsModalOpen(true);
  };

  const handleOk = () => {
    setIsModalOpen(false);
  };

  const handleCancel = () => {
    setIsModalOpen(false);
  };

  const handleAccountClick = (account: ModelProvider) => {
    onSelectAccount(account);
    setProviderAccount(account);
    setIsModalOpen(false);
  };

  const {
    openArgs: modalOpenArgs,
    setOpenArgs: setModalOpenArgs,
    setOpen: setModalOpen,
  } = useOverlay<AddAccountModalOpenArgs>();

  if (loading) {
    return <></>;
  }

  const AccountItem: React.FC<{
    account: ModelProvider;
    onClick: () => void;
  }> = ({ account, onClick }) => {
    return (
      <div
        key={account.provider}
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
        }}
        className={styles["account-item"]}
        onClick={onClick}
      >
        <div style={{ display: "flex", flexDirection: "column" }}>
          <Omission item={account.provider || ""} start={6} end={4} />
        </div>
        <p>{((account.balance || 0) / 10 ** 18).toPrecision(2)} A0GI</p>
      </div>
    );
  };

  const Omission: React.FC<{ item: string; start: number; end: number }> = ({
    item,
    start,
    end,
  }) => {
    return (
      <p className={styles.item}>
        <span className={styles.start}>{(item || []).slice(0, start)}</span>
        <span className={styles.middle}>...</span>
        <span className={styles.end}>{(item || []).slice(-end)}</span>
      </p>
    );
  };

  return (
    <div style={{ height: "100%", display: "flex", justifyContent: "center" }}>
      <a onClick={showModal} style={{ display: "flex", alignItems: "center" }}>
        {providerAccount ? (
          <Omission item={providerAccount.provider || ""} start={6} end={4} />
        ) : (
          "Add Account"
        )}
        <DownOutlined />
      </a>
      <Modal
        title="Provider Account"
        open={isModalOpen}
        onOk={handleOk}
        onCancel={handleCancel}
        footer={null}
        loading={loading}
      >
        <div className={styles["modal-content"]}>
          {providerAccounts.length > 0 ? (
            providerAccounts.map((account) => (
              <AccountItem
                key={account.provider}
                account={account}
                onClick={() => {
                  handleAccountClick(account);
                }}
              />
            ))
          ) : (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
          )}
        </div>
        <Button
          key="btn-edit"
          shape="round"
          onClick={() => setModalOpenArgs({ open: true, args: {} })}
          style={{ width: "100%" }}
        >
          Add account
        </Button>
      </Modal>
      <AddAccountModal
        openArgs={modalOpenArgs}
        onOk={() => {
          setModalOpen(false);
          listProviderAccount().then((data) => {
            setLoading(false);
            setProviderAccounts(data.items || []);
          }, alertError);
        }}
        onCancel={() => setModalOpen(false)}
      />
    </div>
  );
};

export default AccountOverview;
