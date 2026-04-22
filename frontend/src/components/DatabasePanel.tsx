import React, { useState, useCallback, useRef, useEffect } from "react";
import { useTranslation } from "../i18n";
import {
  Database, Plus, Trash2, RefreshCw, Play, ChevronRight, ChevronDown,
  Table2, Eye, X, Loader2, AlertTriangle, Link,
} from "lucide-react";
import {
  Connect, Disconnect, ListTables, DescribeTable, RunQuery,
  ConnectionConfig, ConnectionInfo, TableInfo, ColumnInfo, QueryResult,
} from "../bindings/database";
import { SelectFile } from "../bindings/filesystem";

interface ConnForm {
  name: string;
  type: "sqlite" | "mysql" | "postgres";
  host: string;
  port: string;
  user: string;
  password: string;
  database: string;
  filePath: string;
}

const DEFAULT_FORM: ConnForm = {
  name: "", type: "sqlite", host: "localhost", port: "",
  user: "", password: "", database: "", filePath: "",
};

const TYPE_LABELS: Record<string, string> = {
  sqlite: "SQLite", mysql: "MySQL", postgres: "PostgreSQL",
};

export default function DatabasePanel() {
  const [connections, setConnections] = useState<ConnectionInfo[]>([]);
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState<ConnForm>(DEFAULT_FORM);
  const [connecting, setConnecting] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const [expandedConns, setExpandedConns] = useState<Set<string>>(new Set());
  const [tables, setTables] = useState<Record<string, TableInfo[]>>({});
  const [tablesLoading, setTablesLoading] = useState<Set<string>>(new Set());
  const [expandedTables, setExpandedTables] = useState<Set<string>>(new Set());
  const [columns, setColumns] = useState<Record<string, ColumnInfo[]>>({});

  const [activeConn, setActiveConn] = useState<string | null>(null);
  const [queryText, setQueryText] = useState("");
  const [queryResult, setQueryResult] = useState<QueryResult | null>(null);
  const [queryRunning, setQueryRunning] = useState(false);

  const loadTables = useCallback(async (connId: string) => {
    setTablesLoading((s) => new Set([...s, connId]));
    try {
      const t = await ListTables(connId);
      setTables((prev) => ({ ...prev, [connId]: t ?? [] }));
    } catch (e) {
      console.error("listTables", e);
    } finally {
      setTablesLoading((s) => { const n = new Set(s); n.delete(connId); return n; });
    }
  }, []);

  const toggleConn = useCallback(async (connId: string) => {
    setExpandedConns((s) => {
      const n = new Set(s);
      if (n.has(connId)) { n.delete(connId); return n; }
      n.add(connId);
      return n;
    });
    if (!tables[connId]) await loadTables(connId);
  }, [tables, loadTables]);

  const toggleTable = useCallback(async (connId: string, tableName: string) => {
    const key = `${connId}::${tableName}`;
    setExpandedTables((s) => {
      const n = new Set(s); n.has(key) ? n.delete(key) : n.add(key); return n;
    });
    if (!columns[key]) {
      try {
        const cols = await DescribeTable(connId, tableName);
        setColumns((prev) => ({ ...prev, [key]: cols ?? [] }));
      } catch (e) { console.error("describeTable", e); }
    }
  }, [columns]);

  const handleConnect = useCallback(async () => {
    if (!form.name.trim()) { setFormError(t("database.nameRequired")); return; }
    if (form.type === "sqlite" && !form.filePath) { setFormError(t("database.selectFile")); return; }
    if (form.type !== "sqlite" && (!form.host || !form.user)) { setFormError(t("database.hostUserRequired")); return; }
    setFormError(null);
    setConnecting(true);
    const cfg: ConnectionConfig = {
      id: "", name: form.name.trim(), type: form.type,
      host: form.host, port: parseInt(form.port) || 0,
      user: form.user, password: form.password,
      database: form.database, filePath: form.filePath,
    };
    try {
      const info = await Connect(cfg);
      setConnections((prev) => [...prev.filter((c) => c.id !== info.id), info]);
      setShowForm(false);
      setForm(DEFAULT_FORM);
      setExpandedConns((s) => new Set([...s, info.id]));
      await loadTables(info.id);
    } catch (e) {
      setFormError(String(e));
    } finally {
      setConnecting(false);
    }
  }, [form, loadTables]);

  const handleDisconnect = useCallback(async (id: string) => {
    try {
      await Disconnect(id);
      setConnections((prev) => prev.filter((c) => c.id !== id));
      setTables((prev) => { const n = { ...prev }; delete n[id]; return n; });
      if (activeConn === id) setActiveConn(null);
    } catch (e) { alert(String(e)); }
  }, [activeConn]);

  const runQuery = useCallback(async () => {
    if (!activeConn || !queryText.trim()) return;
    setQueryRunning(true);
    setQueryResult(null);
    try {
      const r = await RunQuery(activeConn, queryText.trim(), 500);
      setQueryResult(r);
    } catch (e) {
      setQueryResult({ columns: [], rows: [], rowCount: 0, error: String(e) });
    } finally {
      setQueryRunning(false);
    }
  }, [activeConn, queryText]);

  const quickQuery = useCallback(async (connId: string, tableName: string) => {
    setActiveConn(connId);
    const q = `SELECT * FROM "${tableName}" LIMIT 100`;
    setQueryText(q);
    setQueryRunning(true);
    setQueryResult(null);
    try {
      const r = await RunQuery(connId, q, 500);
      setQueryResult(r);
    } catch (e) {
      setQueryResult({ columns: [], rows: [], rowCount: 0, error: String(e) });
    } finally {
      setQueryRunning(false);
    }
  }, []);

  const { t } = useTranslation();
  return (
    <div className="db-panel">
      {/* ── Header ── */}
      <div className="db-panel-header">
        <Database size={13} className="db-panel-icon" />
        <span className="db-panel-title">{t("database.title")}</span>
        <button className="sidebar-header-btn sidebar-header-btn-always" onClick={() => { setShowForm(!showForm); setFormError(null); }} title={t("database.add")}>
          <Plus size={12} />
        </button>
      </div>

      {/* ── New connection form ── */}
      {showForm && (
        <div className="db-form">
          <div className="db-form-row">
            <label>{t("database.type")}</label>
            <select value={form.type} onChange={(e) => setForm({ ...form, type: e.target.value as ConnForm["type"], port: e.target.value === "mysql" ? "3306" : e.target.value === "postgres" ? "5432" : "" })}>
              <option value="sqlite">SQLite</option>
              <option value="mysql">MySQL</option>
              <option value="postgres">PostgreSQL</option>
            </select>
          </div>
          <div className="db-form-row">
            <label>{t("database.name")}</label>
            <input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder={t("database.name")} />
          </div>
          {form.type === "sqlite" ? (
            <div className="db-form-row">
              <label>{t("database.file")}</label>
              <div style={{ display: "flex", gap: 4, flex: 1 }}>
                <input value={form.filePath} onChange={(e) => setForm({ ...form, filePath: e.target.value })} placeholder={t("database.filePath")} style={{ flex: 1 }} />
                <button className="btn-ghost" style={{ padding: "2px 6px", fontSize: 11 }} onClick={async () => {
                  const p = await SelectFile(t("database.selectFile"), "db,sqlite,sqlite3");
                  if (p) setForm({ ...form, filePath: p });
                }}>{t("database.browse")}</button>
              </div>
            </div>
          ) : (
            <>
              <div className="db-form-row">
                <label>{t("database.host")}</label>
                <input value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} placeholder="localhost" />
              </div>
              <div className="db-form-row">
                <label>{t("database.port")}</label>
                <input value={form.port} onChange={(e) => setForm({ ...form, port: e.target.value })} placeholder={form.type === "mysql" ? "3306" : "5432"} style={{ width: 80 }} />
              </div>
              <div className="db-form-row">
                <label>{t("database.username")}</label>
                <input value={form.user} onChange={(e) => setForm({ ...form, user: e.target.value })} placeholder="root" />
              </div>
              <div className="db-form-row">
                <label>{t("database.password")}</label>
                <input type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} placeholder="••••" />
              </div>
              <div className="db-form-row">
                <label>{t("database.databaseName")}</label>
                <input value={form.database} onChange={(e) => setForm({ ...form, database: e.target.value })} placeholder={t("database.dbNamePlaceholder")} />
              </div>
            </>
          )}
          {formError && <div className="db-form-error"><AlertTriangle size={11} />{formError}</div>}
          <div className="db-form-actions">
            <button className="btn-ghost" onClick={() => { setShowForm(false); setForm(DEFAULT_FORM); setFormError(null); }}>{t("database.cancel")}</button>
            <button className="btn-primary" onClick={handleConnect} disabled={connecting}>
              {connecting ? <Loader2 size={11} className="spin" /> : <Link size={11} />}
              {t("database.connect")}
            </button>
          </div>
        </div>
      )}

      {/* ── Connection tree ── */}
      <div className="db-tree">
        {connections.length === 0 && !showForm && (
          <div className="db-empty">
            <Database size={28} strokeWidth={1} />
            <span>{t("database.clickToAdd")}</span>
          </div>
        )}
        {connections.map((conn) => {
          const isExp = expandedConns.has(conn.id);
          const isLoading = tablesLoading.has(conn.id);
          return (
            <div key={conn.id} className="db-conn-group">
              <div
                className={`db-conn-row${activeConn === conn.id ? " active" : ""}`}
                onClick={() => { toggleConn(conn.id); setActiveConn(conn.id); }}
              >
                <span className="db-tree-arrow">
                  {isLoading ? <Loader2 size={10} className="spin" /> : isExp ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
                </span>
                <Database size={12} className="db-conn-icon" />
                <span className="db-conn-name">{conn.name}</span>
                <span className="db-conn-type">{TYPE_LABELS[conn.type]}</span>
                <button className="db-icon-btn" title={t("database.refresh")} onClick={(e) => { e.stopPropagation(); loadTables(conn.id); }}>
                  <RefreshCw size={10} />
                </button>
                <button className="db-icon-btn db-icon-btn-danger" title={t("database.disconnect")} onClick={(e) => { e.stopPropagation(); handleDisconnect(conn.id); }}>
                  <X size={10} />
                </button>
              </div>
              {isExp && (tables[conn.id] ?? []).map((tbl) => {
                const key = `${conn.id}::${tbl.name}`;
                const isTblExp = expandedTables.has(key);
                return (
                  <div key={tbl.name}>
                    <div className="db-table-row" onClick={() => toggleTable(conn.id, tbl.name)}>
                      <span className="db-tree-arrow" style={{ paddingLeft: 16 }}>
                        {isTblExp ? <ChevronDown size={9} /> : <ChevronRight size={9} />}
                      </span>
                      {tbl.type === "view" ? <Eye size={11} className="db-table-icon" /> : <Table2 size={11} className="db-table-icon" />}
                      <span className="db-table-name">{tbl.name}</span>
                      <button className="db-icon-btn" title={t("database.viewData")} onClick={(e) => { e.stopPropagation(); quickQuery(conn.id, tbl.name); }}>
                        <Play size={9} />
                      </button>
                    </div>
                    {isTblExp && (columns[key] ?? []).map((col) => (
                      <div key={col.name} className="db-col-row">
                        <span className="db-col-key">{col.key === "PRI" ? "🔑" : "·"}</span>
                        <span className="db-col-name">{col.name}</span>
                        <span className="db-col-type">{col.type}</span>
                        {!col.nullable && <span className="db-col-badge">NN</span>}
                      </div>
                    ))}
                  </div>
                );
              })}
            </div>
          );
        })}
      </div>

      {/* ── Query panel ── */}
      {connections.length > 0 && (
        <div className="db-query-panel">
          <div className="db-query-header">
            <span className="db-query-title">{t("database.sqlQuery")}</span>
            {connections.length > 1 && (
              <select
                className="db-conn-select"
                value={activeConn ?? ""}
                onChange={(e) => setActiveConn(e.target.value)}
              >
                <option value="">{t("database.selectConn")}</option>
                {connections.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
              </select>
            )}
            <button className="btn-primary" style={{ padding: "3px 10px", fontSize: 11 }} onClick={runQuery} disabled={queryRunning || !activeConn}>
              {queryRunning ? <Loader2 size={11} className="spin" /> : <Play size={11} />}
              {t("database.execute")}
            </button>
          </div>
          <textarea
            className="db-query-input"
            value={queryText}
            onChange={(e) => setQueryText(e.target.value)}
            placeholder={t("database.sqlPlaceholder")}
            onKeyDown={(e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); runQuery(); } }}
            rows={4}
          />
          {queryResult && (
            <div className="db-result">
              {queryResult.error ? (
                <div className="db-result-error"><AlertTriangle size={12} />{queryResult.error}</div>
              ) : (
                <>
                  <div className="db-result-meta">{queryResult.rowCount} {t("database.rows")}</div>
                  <div className="db-result-table-wrap">
                    <table className="db-result-table">
                      <thead>
                        <tr>{queryResult.columns.map((c) => <th key={c}>{c}</th>)}</tr>
                      </thead>
                      <tbody>
                        {queryResult.rows.map((row, i) => (
                          <tr key={i}>{row.map((cell, j) => (
                            <td key={j}>{cell === null ? <span className="db-null">NULL</span> : String(cell)}</td>
                          ))}</tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
