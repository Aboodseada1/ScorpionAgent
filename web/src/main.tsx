import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import { MotionConfig } from "framer-motion";
import "./index.css";
import App from "./App";
import ClientsPage from "./pages/Clients";
import ClientDetailPage from "./pages/ClientDetail";
import CallPage from "./pages/Call";
import MemoryPage from "./pages/Memory";
import SettingsPage from "./pages/Settings";
import MonitorPage from "./pages/Monitor";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <BrowserRouter>
      <MotionConfig reducedMotion="user">
      <Routes>
        <Route path="/" element={<App />}>
          <Route index element={<Navigate to="/clients" replace />} />
          <Route path="clients" element={<ClientsPage />} />
          <Route path="clients/:id" element={<ClientDetailPage />} />
          <Route path="clients/:id/call" element={<CallPage />} />
          <Route path="memory" element={<MemoryPage />} />
          <Route path="monitor" element={<MonitorPage />} />
          <Route path="settings" element={<SettingsPage />} />
        </Route>
      </Routes>
      </MotionConfig>
    </BrowserRouter>
  </React.StrictMode>
);
