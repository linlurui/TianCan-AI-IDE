import React, { useState, useEffect, useCallback, useRef } from "react";
import { useTranslation } from "../i18n";
import {
  Server, Plus, Trash2, RefreshCw, Play, Loader2, CheckCircle2,
  XCircle, Clock, Settings, ChevronRight, ChevronDown,
  Upload, Container, Box, AlertTriangle, Wifi, WifiOff,
} from "lucide-react";
import {
  AddServer, UpdateServer, RemoveServer, ListServers, TestConnection,
  AddConfig, UpdateConfig, RemoveConfig, ListConfigs,
  Deploy, GetTask, ListTasks,
  ServerConfig, DeployConfig, DeployTask,
} from "../bindings/deploy";

type WorkTab = "servers" | "configs" | "logs";
type DeployType = "shell" | "docker" | "k8s";
type AuthType = "password" | "key";

const DEPLOY_TYPE_KEYS: Record<string, string> = { shell: "deploy.shell", docker: "deploy.docker", k8s: "deploy.k8s" };
const STATUS_ICON: Record<string, React.ReactNode> = {
  idle: <Clock size={13} className="dep-status-idle" />,
  building: <Loader2 size={13} className="dep-status-building spin" />,
  uploading: <Upload size={13} className="dep-status-uploading spin" />,
  deploying: <Play size={13} className="dep-status-deploying spin" />,
  done: <CheckCircle2 size={13} className="dep-status-done" />,
  error: <XCircle size={13} className="dep-status-error" />,
};

function emptyServer(): ServerConfig {
  return { id: "", name: "", host: "", port: 22, user: "root", authType: "password", password: "", keyPath: "", tags: [] };
}

function emptyConfig(): DeployConfig {
  return {
    id: "", name: "", type: "shell", serverIds: [],
    buildCmd: "", localDir: "", remoteDir: "/app",
    script: "#!/bin/bash\necho \"Deploy started...\"\n# Add deploy commands here\necho \"Deploy complete!\"",
    dockerImage: "app:latest", dockerfile: "Dockerfile",
    registryURL: "", k8sManifest: "", k8sNamespace: "default", k8sContext: "",
  };
}

// ── Server form ──────────────────────────────────────────────

function ServerForm({ initial, servers, onSave, onCancel }: {
  initial: ServerConfig; servers: ServerConfig[];
  onSave: (s: ServerConfig) => void; onCancel: () => void;
}) {
  const [form, setForm] = useState<ServerConfig>(initial);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<string | null>(null);
  const { t } = useTranslation();

  const upd = (p: Partial<ServerConfig>) => setForm((f) => ({ ...f, ...p }));

  const handleTest = async () => {
    if (!form.id) { setTestResult(t("deploy.saveFirst")); return; }
    setTesting(true); setTestResult(null);
    try {
      await TestConnection(form.id);
      setTestResult(t("deploy.connSuccess"));
    } catch (e) {
      setTestResult("❌ " + String(e));
    } finally { setTesting(false); }
  };

  return (
    <div className="dep-form">
      <div className="dep-form-row"><label>{t("deploy.name")}</label><input value={form.name} onChange={(e) => upd({ name: e.target.value })} placeholder={t("deploy.serverPlaceholder")} /></div>
      <div className="dep-form-row"><label>{t("deploy.host")}</label><input value={form.host} onChange={(e) => upd({ host: e.target.value })} placeholder="192.168.1.100" /></div>
      <div className="dep-form-row"><label>{t("deploy.port")}</label><input type="number" value={form.port} onChange={(e) => upd({ port: parseInt(e.target.value) || 22 })} style={{ width: 80 }} /></div>
      <div className="dep-form-row"><label>{t("deploy.user")}</label><input value={form.user} onChange={(e) => upd({ user: e.target.value })} placeholder="root" /></div>
      <div className="dep-form-row">
        <label>{t("deploy.auth")}</label>
        <select value={form.authType} onChange={(e) => upd({ authType: e.target.value as AuthType })}>
          <option value="password">{t("deploy.password")}</option>
          <option value="key">{t("deploy.sshKey")}</option>
        </select>
      </div>
      {form.authType === "password"
        ? <div className="dep-form-row"><label>{t("deploy.password")}</label><input type="password" value={form.password} onChange={(e) => upd({ password: e.target.value })} placeholder="••••" /></div>
        : <div className="dep-form-row"><label>{t("deploy.keyPath")}</label><input value={form.keyPath} onChange={(e) => upd({ keyPath: e.target.value })} placeholder="~/.ssh/id_rsa" /></div>}
      <div className="dep-form-row"><label>{t("deploy.tags")}</label><input value={form.tags?.join(",")} onChange={(e) => upd({ tags: e.target.value.split(",").map((t) => t.trim()).filter(Boolean) })} placeholder="prod,web" /></div>
      {testResult && <div className="dep-test-result">{testResult}</div>}
      <div className="dep-form-actions">
        <button className="btn-ghost-sm" onClick={handleTest} disabled={testing}>
          {testing ? <Loader2 size={11} className="spin" /> : <Wifi size={11} />} {t("deploy.testConn")}
        </button>
        <div style={{ flex: 1 }} />
        <button className="btn-ghost" onClick={onCancel}>{t("deploy.cancel")}</button>
        <button className="btn-primary" onClick={() => onSave(form)}>{t("deploy.save")}</button>
      </div>
    </div>
  );
}

// ── Config form ──────────────────────────────────────────────

function ConfigForm({ initial, servers, onSave, onCancel }: {
  initial: DeployConfig; servers: ServerConfig[];
  onSave: (c: DeployConfig) => void; onCancel: () => void;
}) {
  const [form, setForm] = useState<DeployConfig>(initial);
  const { t } = useTranslation();
  const upd = (p: Partial<DeployConfig>) => setForm((f) => ({ ...f, ...p }));

  const toggleServer = (id: string) => {
    const ids = form.serverIds ?? [];
    upd({ serverIds: ids.includes(id) ? ids.filter((s) => s !== id) : [...ids, id] });
  };

  return (
    <div className="dep-form dep-config-form">
      <div className="dep-form-row"><label>{t("deploy.name")}</label><input value={form.name} onChange={(e) => upd({ name: e.target.value })} placeholder={t("deploy.deployPlaceholder")} /></div>
      <div className="dep-form-row">
        <label>{t("deploy.type")}</label>
        <select value={form.type} onChange={(e) => upd({ type: e.target.value as DeployType })}>
          <option value="shell">{t("deploy.shell")}</option>
          <option value="docker">{t("deploy.docker")}</option>
          <option value="k8s">{t("deploy.k8s")}</option>
        </select>
      </div>

      <div className="dep-form-section">{t("deploy.targetServer")}</div>
      {servers.length === 0
        ? <div className="dep-form-hint">{t("deploy.addServerHint")}</div>
        : <div className="dep-server-checks">
            {servers.map((sv) => (
              <label key={sv.id} className="dep-server-check">
                <input type="checkbox" checked={(form.serverIds ?? []).includes(sv.id)} onChange={() => toggleServer(sv.id)} />
                <Server size={11} /><span>{sv.name}</span>
                <span className="dep-sv-host">{sv.user}@{sv.host}</span>
              </label>
            ))}
          </div>
      }

      <div className="dep-form-section">{t("deploy.buildUpload")}</div>
      <div className="dep-form-row"><label>{t("deploy.buildCmd")}</label><input value={form.buildCmd} onChange={(e) => upd({ buildCmd: e.target.value })} placeholder="npm run build" /></div>
      <div className="dep-form-row"><label>{t("deploy.localDir")}</label><input value={form.localDir} onChange={(e) => upd({ localDir: e.target.value })} placeholder="/path/to/dist" /></div>
      <div className="dep-form-row"><label>{t("deploy.remoteDir")}</label><input value={form.remoteDir} onChange={(e) => upd({ remoteDir: e.target.value })} placeholder="/app" /></div>

      {form.type === "shell" && (
        <>
          <div className="dep-form-section">{t("deploy.deployScript")}</div>
          <textarea className="dep-script-editor" value={form.script} onChange={(e) => upd({ script: e.target.value })} rows={8} spellCheck={false} />
        </>
      )}

      {form.type === "docker" && (
        <>
          <div className="dep-form-section">{t("deploy.dockerConfig")}</div>
          <div className="dep-form-row"><label>{t("deploy.imageName")}</label><input value={form.dockerImage} onChange={(e) => upd({ dockerImage: e.target.value })} placeholder="app:latest" /></div>
          <div className="dep-form-row"><label>Dockerfile</label><input value={form.dockerfile} onChange={(e) => upd({ dockerfile: e.target.value })} placeholder="Dockerfile" /></div>
          <div className="dep-form-row"><label>Registry</label><input value={form.registryURL} onChange={(e) => upd({ registryURL: e.target.value })} placeholder="registry.example.com" /></div>
          <div className="dep-form-section">{t("deploy.dockerCmd")}</div>
          <textarea className="dep-script-editor" value={form.script} onChange={(e) => upd({ script: e.target.value })} rows={5}
            placeholder="docker stop app || true&#10;docker rm app || true&#10;docker run -d --name app -p 80:80 app:latest" spellCheck={false} />
        </>
      )}

      {form.type === "k8s" && (
        <>
          <div className="dep-form-section">{t("deploy.k8sConfig")}</div>
          <div className="dep-form-row"><label>Namespace</label><input value={form.k8sNamespace} onChange={(e) => upd({ k8sNamespace: e.target.value })} placeholder="default" /></div>
          <div className="dep-form-row"><label>Context</label><input value={form.k8sContext} onChange={(e) => upd({ k8sContext: e.target.value })} placeholder="my-cluster" /></div>
          <div className="dep-form-section">{t("deploy.manifestYaml")}</div>
          <textarea className="dep-script-editor dep-yaml-editor" value={form.k8sManifest} onChange={(e) => upd({ k8sManifest: e.target.value })}
            rows={12} placeholder={"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app\nspec:\n  replicas: 2\n  ..."} spellCheck={false} />
        </>
      )}

      <div className="dep-form-actions">
        <button className="btn-ghost" onClick={onCancel}>{t("deploy.cancel")}</button>
        <button className="btn-primary" onClick={() => onSave(form)}>{t("deploy.save")}</button>
      </div>
    </div>
  );
}

// ── Main component ────────────────────────────────────────────

export default function DeployWorkspace() {
  const { t } = useTranslation();
  const [workTab, setWorkTab] = useState<WorkTab>("servers");
  const [servers, setServers] = useState<ServerConfig[]>([]);
  const [configs, setConfigs] = useState<DeployConfig[]>([]);
  const [tasks, setTasks] = useState<DeployTask[]>([]);
  const [activeTask, setActiveTask] = useState<DeployTask | null>(null);

  const [editServer, setEditServer] = useState<ServerConfig | null>(null);
  const [editConfig, setEditConfig] = useState<DeployConfig | null>(null);

  const [deploying, setDeploying] = useState<Record<string, boolean>>({});
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const reload = useCallback(async () => {
    const [svs, cfgs, tks] = await Promise.all([ListServers(), ListConfigs(), ListTasks()]);
    setServers(svs ?? []);
    setConfigs(cfgs ?? []);
    setTasks((tks ?? []).sort((a: DeployTask, b: DeployTask) => b.startTime.localeCompare(a.startTime)));
  }, []);

  useEffect(() => { reload(); }, [reload]);

  useEffect(() => {
    const running = tasks.some((t) => ["building", "uploading", "deploying"].includes(t.status));
    if (running) {
      if (!pollRef.current) pollRef.current = setInterval(reload, 1500);
    } else {
      if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null; }
    }
    return () => { if (pollRef.current) clearInterval(pollRef.current); };
  }, [tasks, reload]);

  const saveServer = async (sv: ServerConfig) => {
    if (sv.id) { await UpdateServer(sv); } else { const r = await AddServer(sv); sv = r; }
    setEditServer(null);
    await reload();
  };

  const deleteServer = async (id: string) => {
    if (!confirm(t("deploy.confirmDeleteServer"))) return;
    await RemoveServer(id);
    await reload();
  };

  const saveConfig = async (cfg: DeployConfig) => {
    if (cfg.id) { await UpdateConfig(cfg); } else { await AddConfig(cfg); }
    setEditConfig(null);
    await reload();
  };

  const deleteConfig = async (id: string) => {
    if (!confirm(t("deploy.confirmDeleteConfig"))) return;
    await RemoveConfig(id);
    await reload();
  };

  const handleDeploy = async (cfg: DeployConfig) => {
    setDeploying((d) => ({ ...d, [cfg.id]: true }));
    try {
      const taskId = await Deploy(cfg.id);
      setWorkTab("logs");
      await reload();
      const t = await GetTask(taskId);
      setActiveTask(t);
    } catch (e) {
      alert(t("deploy.deployFail") + ": " + String(e));
    } finally {
      setDeploying((d) => ({ ...d, [cfg.id]: false }));
    }
  };

  const refreshTask = async (id: string) => {
    const t = await GetTask(id);
    setActiveTask(t);
    await reload();
  };

  const typeIcon = (t: string) =>
    t === "docker" ? <Container size={12} /> : t === "k8s" ? <Box size={12} /> : <Server size={12} />;

  return (
    <div className="dep-workspace">
      {/* ── Top tabs ── */}
      <div className="dep-top-tabs">
        {(["servers", "configs", "logs"] as WorkTab[]).map((tab) => (
          <button key={tab} className={`dep-top-tab${workTab === tab ? " active" : ""}`} onClick={() => setWorkTab(tab)}>
            {tab === "servers" ? <><Server size={13} /> {t("deploy.serverMgmt")}</> : tab === "configs" ? <><Settings size={13} /> {t("deploy.configs")}</> : <><Clock size={13} /> {t("deploy.logs")}</>}
          </button>
        ))}
        <div style={{ flex: 1 }} />
        <button className="btn-ghost-sm" onClick={reload} title={t("deploy.refresh")}><RefreshCw size={12} /></button>
      </div>

      <div className="dep-content">
        {/* ── Servers tab ── */}
        {workTab === "servers" && (
          <div className="dep-list-layout">
            <div className="dep-list-header">
              <span className="dep-list-title">{t("deploy.serverList")}</span>
              <button className="btn-primary" style={{ padding: "3px 10px", fontSize: 11 }}
                onClick={() => setEditServer(emptyServer())}>
                <Plus size={12} /> {t("deploy.addServer")}
              </button>
            </div>
            {editServer && (
              <div className="dep-form-wrap">
                <div className="dep-form-title">{editServer.id ? t("deploy.editServer") : t("deploy.newServer")}</div>
                <ServerForm initial={editServer} servers={servers}
                  onSave={saveServer} onCancel={() => setEditServer(null)} />
              </div>
            )}
            <div className="dep-card-list">
              {servers.length === 0 && !editServer && (
                <div className="dep-empty"><Server size={32} strokeWidth={1} /><span>{t("deploy.noServers")}</span></div>
              )}
              {servers.map((sv) => (
                <div key={sv.id} className="dep-server-card">
                  <Server size={16} className="dep-card-icon" />
                  <div className="dep-card-info">
                    <span className="dep-card-name">{sv.name}</span>
                    <span className="dep-card-sub">{sv.user}@{sv.host}:{sv.port}</span>
                    <div className="dep-tag-row">{(sv.tags ?? []).map((t) => <span key={t} className="dep-tag">{t}</span>)}</div>
                  </div>
                  <div className="dep-card-actions">
                    <button className="dep-act-btn" title={t("deploy.edit")} onClick={() => setEditServer({ ...sv })}><Settings size={12} /></button>
                    <button className="dep-act-btn dep-act-danger" title={t("deploy.delete")} onClick={() => deleteServer(sv.id)}><Trash2 size={12} /></button>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* ── Configs tab ── */}
        {workTab === "configs" && (
          <div className="dep-list-layout">
            <div className="dep-list-header">
              <span className="dep-list-title">{t("deploy.configList")}</span>
              <button className="btn-primary" style={{ padding: "3px 10px", fontSize: 11 }}
                onClick={() => setEditConfig(emptyConfig())}>
                <Plus size={12} /> {t("deploy.newConfig")}
              </button>
            </div>
            {editConfig && (
              <div className="dep-form-wrap">
                <div className="dep-form-title">{editConfig.id ? t("deploy.editConfig") : t("deploy.newConfig")}</div>
                <ConfigForm initial={editConfig} servers={servers}
                  onSave={saveConfig} onCancel={() => setEditConfig(null)} />
              </div>
            )}
            <div className="dep-card-list">
              {configs.length === 0 && !editConfig && (
                <div className="dep-empty"><Settings size={32} strokeWidth={1} /><span>{t("deploy.noConfigs")}</span></div>
              )}
              {configs.map((cfg) => {
                const connServers = servers.filter((sv) => (cfg.serverIds ?? []).includes(sv.id));
                return (
                  <div key={cfg.id} className="dep-config-card">
                    <div className="dep-config-card-icon">{typeIcon(cfg.type)}</div>
                    <div className="dep-card-info">
                      <div className="dep-config-card-top">
                        <span className="dep-card-name">{cfg.name}</span>
                        <span className="dep-type-badge">{t(DEPLOY_TYPE_KEYS[cfg.type])}</span>
                      </div>
                      <div className="dep-config-servers">
                        {connServers.length === 0
                          ? <span className="dep-no-server">{t("deploy.noBoundServer")}</span>
                          : connServers.map((sv) => <span key={sv.id} className="dep-sv-chip"><Server size={9} />{sv.name}</span>)
                        }
                      </div>
                      {cfg.buildCmd && <span className="dep-card-sub">{t("deploy.build")}: {cfg.buildCmd}</span>}
                    </div>
                    <div className="dep-card-actions">
                      <button className="dep-deploy-btn" onClick={() => handleDeploy(cfg)} disabled={deploying[cfg.id]}>
                        {deploying[cfg.id] ? <Loader2 size={12} className="spin" /> : <Play size={12} />}
                        {t("deploy.deploy")}
                      </button>
                      <button className="dep-act-btn" title={t("deploy.edit")} onClick={() => setEditConfig({ ...cfg })}><Settings size={12} /></button>
                      <button className="dep-act-btn dep-act-danger" title={t("deploy.delete")} onClick={() => deleteConfig(cfg.id)}><Trash2 size={12} /></button>
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {/* ── Logs tab ── */}
        {workTab === "logs" && (
          <div className="dep-logs-layout">
            <div className="dep-logs-sidebar">
              <div className="dep-logs-sidebar-title">{t("deploy.deployHistory")}</div>
              {tasks.length === 0 && <div className="dep-empty-sm">{t("deploy.noDeployRecords")}</div>}
              {tasks.map((t) => (
                <div key={t.id} className={`dep-task-item${activeTask?.id === t.id ? " active" : ""}`}
                  onClick={() => { setActiveTask(t); refreshTask(t.id); }}>
                  <div className="dep-task-item-top">
                    {STATUS_ICON[t.status]}
                    <span className="dep-task-name">{t.configName}</span>
                  </div>
                  <span className="dep-task-time">{t.startTime?.slice(11, 19)}</span>
                </div>
              ))}
            </div>
            <div className="dep-log-panel">
              {!activeTask
                ? <div className="dep-empty"><Clock size={32} strokeWidth={1} /><span>{t("deploy.selectRecord")}</span></div>
                : <>
                    <div className="dep-log-header">
                      <span className="dep-log-config">{activeTask.configName}</span>
                      {STATUS_ICON[activeTask.status]}
                      <span className={`dep-log-status dep-log-status-${activeTask.status}`}>
                        {t(`deploy.${activeTask.status}`) ?? activeTask.status}
                      </span>
                      <div style={{ flex: 1 }} />
                      <button className="btn-ghost-sm" onClick={() => refreshTask(activeTask.id)} title={t("deploy.refreshLog")}>
                        <RefreshCw size={11} />
                      </button>
                    </div>
                    <pre className="dep-log-output">{activeTask.log || `(${t("deploy.waitingOutput")})`}</pre>
                  </>
              }
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
