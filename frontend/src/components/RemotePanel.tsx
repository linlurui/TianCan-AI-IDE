/**
 * RemotePanel — 远程开发面板
 * 支持 SSH 连接和 Docker 容器连接
 */
import React, { useState, useEffect, useCallback } from "react";
import { useTranslation } from "../i18n";
import { Server, Container, Plus, Trash2, Plug, Unplug, FolderOpen } from "lucide-react";
import {
  GetConnections, AddConnection, RemoveConnection,
  Connect, Disconnect, GetConnectionStatus, ListDockerContainers,
} from "@bindings/remote";
import { ConnectionConfig, ConnectionStatus, DockerContainer } from "../../bindings/github.com/linlurui/tiancan-ai-ide/backend/remote/models";

type ConnectionType = "ssh" | "docker";

interface Props {
  onOpenRemoteFile?: (connID: string, path: string, type: "ssh" | "docker") => void;
}

function AddConnectionForm({ onAdd, onCancel }: {
  onAdd: (cfg: ConnectionConfig) => void;
  onCancel: () => void;
}) {
  const [type, setType] = useState<ConnectionType>("ssh");
  const [name, setName] = useState("");
  const [host, setHost] = useState("");
  const [port, setPort] = useState("22");
  const [user, setUser] = useState("");
  const [password, setPassword] = useState("");
  const [keyPath, setKeyPath] = useState("");
  const { t } = useTranslation();

  return (
    <div className="remote-add-form">
      <div className="remote-form-row">
        <label>{t("remote.type")}</label>
        <select value={type} onChange={(e) => setType(e.target.value as ConnectionType)}>
          <option value="ssh">SSH</option>
          <option value="docker">{t("remote.dockerLocal")}</option>
        </select>
      </div>
      <div className="remote-form-row">
        <label>{t("remote.name")}</label>
        <input placeholder={t("remote.namePlaceholder")} value={name} onChange={(e) => setName(e.target.value)} />
      </div>
      {type === "ssh" && (
        <>
          <div className="remote-form-row">
            <label>{t("remote.host")}</label>
            <input placeholder="192.168.1.100" value={host} onChange={(e) => setHost(e.target.value)} />
          </div>
          <div className="remote-form-row">
            <label>{t("remote.port")}</label>
            <input placeholder="22" value={port} onChange={(e) => setPort(e.target.value)} />
          </div>
          <div className="remote-form-row">
            <label>{t("remote.username")}</label>
            <input placeholder="ubuntu" value={user} onChange={(e) => setUser(e.target.value)} />
          </div>
          <div className="remote-form-row">
            <label>{t("remote.password")}</label>
            <input type="password" placeholder={t("remote.keyPlaceholder")} value={password} onChange={(e) => setPassword(e.target.value)} />
          </div>
          <div className="remote-form-row">
            <label>{t("remote.keyPath")}</label>
            <input placeholder="~/.ssh/id_rsa" value={keyPath} onChange={(e) => setKeyPath(e.target.value)} />
          </div>
        </>
      )}
      <div className="remote-form-actions">
        <button
          className="remote-btn-primary"
          onClick={() => {
            if (!name.trim()) return;
            onAdd(ConnectionConfig.createFrom({
              id: "",
              name: name.trim(),
              type: type as any,
              host,
              port: parseInt(port) || 22,
              user,
              password,
              keyPath,
              containerId: "",
              containerName: "",
            }));
          }}
        >
          {t("remote.add")}
        </button>
        <button className="remote-btn-secondary" onClick={onCancel}>{t("remote.cancel")}</button>
      </div>
    </div>
  );
}

export default function RemotePanel({ onOpenRemoteFile }: Props) {
  const { t } = useTranslation();
  const [connections, setConnections] = useState<ConnectionConfig[]>([]);
  const [statuses, setStatuses] = useState<Record<string, boolean>>({});
  const [dockerContainers, setDockerContainers] = useState<DockerContainer[]>([]);
  const [showAddForm, setShowAddForm] = useState(false);
  const [loading, setLoading] = useState<Record<string, boolean>>({});
  const [error, setError] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<"ssh" | "docker">("ssh");

  const loadConnections = useCallback(async () => {
    try {
      const [conns, sts] = await Promise.all([
        GetConnections(),
        GetConnectionStatus(),
      ]);
      setConnections(conns ?? []);
      const statusMap: Record<string, boolean> = {};
      for (const s of sts ?? []) statusMap[s.id] = s.connected;
      setStatuses(statusMap);
    } catch (e: any) {
      console.warn("remote panel:", e);
    }
  }, []);

  const loadDockerContainers = useCallback(async () => {
    try {
      const containers = await ListDockerContainers();
      setDockerContainers(containers ?? []);
    } catch {
      setDockerContainers([]);
    }
  }, []);

  useEffect(() => {
    loadConnections();
    loadDockerContainers();
  }, [loadConnections, loadDockerContainers]);

  const handleAdd = useCallback(async (cfg: ConnectionConfig) => {
    try {
      await AddConnection(cfg);
      setShowAddForm(false);
      await loadConnections();
    } catch (e: any) {
      setError(e?.message ?? t("remote.addFail"));
    }
  }, [loadConnections]);

  const handleConnect = useCallback(async (id: string) => {
    setLoading((p) => ({ ...p, [id]: true }));
    setError(null);
    try {
      await Connect(id);
      await loadConnections();
    } catch (e: any) {
      setError(e?.message ?? t("remote.connFail"));
    } finally {
      setLoading((p) => ({ ...p, [id]: false }));
    }
  }, [loadConnections]);

  const handleDisconnect = useCallback(async (id: string) => {
    try {
      await Disconnect(id);
      await loadConnections();
    } catch (e: any) {
      setError(e?.message ?? t("remote.disconnFail"));
    }
  }, [loadConnections]);

  const handleRemove = useCallback(async (id: string) => {
    try {
      await RemoveConnection(id);
      await loadConnections();
    } catch (e: any) {
      setError(e?.message ?? t("remote.deleteFail"));
    }
  }, [loadConnections]);

  return (
    <div className="remote-panel">
      {/* Tab 切换 */}
      <div className="remote-tabs">
        <button
          className={`remote-tab${activeTab === "ssh" ? " active" : ""}`}
          onClick={() => setActiveTab("ssh")}
        >
          <Server size={13} /> SSH
        </button>
        <button
          className={`remote-tab${activeTab === "docker" ? " active" : ""}`}
          onClick={() => { setActiveTab("docker"); loadDockerContainers(); }}
        >
          <Container size={13} /> Docker
        </button>
      </div>

      {error && (
        <div className="remote-error">{error}</div>
      )}

      {/* SSH Tab */}
      {activeTab === "ssh" && (
        <div className="remote-ssh-section">
          <div className="remote-section-header">
            <span>{t("remote.sshConn")}</span>
            <button
              className="remote-add-btn"
              onClick={() => setShowAddForm((v) => !v)}
              title={t("remote.addConn")}
            >
              <Plus size={14} />
            </button>
          </div>

          {showAddForm && (
            <AddConnectionForm
              onAdd={handleAdd}
              onCancel={() => setShowAddForm(false)}
            />
          )}

          {connections.filter((c) => c.type === "ssh").length === 0 && !showAddForm && (
            <div className="remote-empty">
              <Server size={32} strokeWidth={1} />
              <span>{t("remote.noSsh")}</span>
              <p>{t("remote.addRemoteHint")}</p>
            </div>
          )}

          {connections
            .filter((c) => c.type === "ssh")
            .map((conn) => {
              const connected = statuses[conn.id];
              const isLoading = loading[conn.id];
              return (
                <div key={conn.id} className={`remote-conn-item${connected ? " connected" : ""}`}>
                  <div className="remote-conn-info">
                    <span className={`remote-conn-dot${connected ? " online" : ""}`} />
                    <div>
                      <div className="remote-conn-name">{conn.name}</div>
                      <div className="remote-conn-addr">
                        {conn.user}@{conn.host}:{conn.port ?? 22}
                      </div>
                    </div>
                  </div>
                  <div className="remote-conn-actions">
                    {connected ? (
                      <>
                        <button
                          className="remote-action-btn"
                          title={t("remote.browseFiles")}
                          onClick={() => onOpenRemoteFile?.(conn.id, "/", "ssh")}
                        >
                          <FolderOpen size={13} />
                        </button>
                        <button
                          className="remote-action-btn"
                          title={t("remote.disconnect")}
                          onClick={() => handleDisconnect(conn.id)}
                        >
                          <Unplug size={13} />
                        </button>
                      </>
                    ) : (
                      <button
                        className="remote-action-btn"
                        title={t("remote.connect")}
                        disabled={isLoading}
                        onClick={() => handleConnect(conn.id)}
                      >
                        {isLoading ? "…" : <Plug size={13} />}
                      </button>
                    )}
                    <button
                      className="remote-action-btn remote-action-btn-danger"
                      title={t("remote.delete")}
                      onClick={() => handleRemove(conn.id)}
                    >
                      <Trash2 size={13} />
                    </button>
                  </div>
                </div>
              );
            })}
        </div>
      )}

      {/* Docker Tab */}
      {activeTab === "docker" && (
        <div className="remote-docker-section">
          <div className="remote-section-header">
            <span>{t("remote.localDocker")}</span>
            <button className="remote-add-btn" onClick={loadDockerContainers} title={t("remote.refresh")}>
              ↺
            </button>
          </div>

          {dockerContainers.length === 0 ? (
            <div className="remote-empty">
              <Container size={32} strokeWidth={1} />
              <span>{t("remote.noContainers")}</span>
              <p>{t("remote.dockerHint")}</p>
            </div>
          ) : (
            dockerContainers.map((c) => (
              <div key={c.id} className="remote-conn-item connected">
                <div className="remote-conn-info">
                  <span className="remote-conn-dot online" />
                  <div>
                    <div className="remote-conn-name">{c.name}</div>
                    <div className="remote-conn-addr">{c.image} · {c.state}</div>
                  </div>
                </div>
                <div className="remote-conn-actions">
                  <button
                    className="remote-action-btn"
                    title={t("remote.browseFiles")}
                    onClick={() => onOpenRemoteFile?.(c.id, "/", "docker")}
                  >
                    <FolderOpen size={13} />
                  </button>
                </div>
              </div>
            ))
          )}
        </div>
      )}
    </div>
  );
}
