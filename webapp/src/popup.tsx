import React from "react";
import ReactDOM from "react-dom/client";
import "./popup.css";
import App from "./App";
import { ConfigProvider } from "antd";
import AntThemeConfig from "./styles/theme";

const root = ReactDOM.createRoot(
  document.getElementById("user-agent-container") as HTMLElement
);
root.render(
  <ConfigProvider theme={AntThemeConfig}>
    <React.StrictMode>
      <App />
    </React.StrictMode>
  </ConfigProvider>
);
