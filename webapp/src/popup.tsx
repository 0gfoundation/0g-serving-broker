import React from "react";
import ReactDOM from "react-dom/client";
import "./popup.css";
import App from "./App";

const root = ReactDOM.createRoot(
  document.getElementById("user-agent-container") as HTMLElement
);
root.render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
