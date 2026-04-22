import React, { useState, useCallback, useRef } from "react";
import { useTranslation } from "../i18n";
import {
  Plus, Send, Trash2, ChevronDown, ChevronRight, Settings,
  Globe, Copy, Check, Loader2, AlertTriangle, Clock, FlaskConical,
} from "lucide-react";

type HttpMethod = "GET" | "POST" | "PUT" | "DELETE" | "PATCH" | "HEAD" | "OPTIONS";
type BodyType = "none" | "json" | "form" | "text";
type ReqTab = "params" | "headers" | "body" | "auth";

interface KV { key: string; value: string; enabled: boolean }
interface SavedRequest {
  id: string;
  name: string;
  method: HttpMethod;
  url: string;
  params: KV[];
  headers: KV[];
  bodyType: BodyType;
  bodyRaw: string;
  bodyForm: KV[];
}
interface GlobalVar { key: string; value: string }
interface ResponseData {
  status: number;
  statusText: string;
  time: number;
  size: string;
  headers: Record<string, string>;
  body: string;
  error?: string;
}

const METHOD_COLORS: Record<HttpMethod, string> = {
  GET: "#61afef", POST: "#98c379", PUT: "#e5c07b",
  DELETE: "#e06c75", PATCH: "#c678dd", HEAD: "#56b6c2", OPTIONS: "#abb2bf",
};

const METHODS: HttpMethod[] = ["GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"];

function newKV(): KV { return { key: "", value: "", enabled: true }; }
function newRequest(): SavedRequest {
  return {
    id: crypto.randomUUID(), name: "New Request",
    method: "GET", url: "",
    params: [newKV()], headers: [newKV()],
    bodyType: "none", bodyRaw: "", bodyForm: [newKV()],
  };
}

function KVEditor({ rows, onChange, label }: { rows: KV[]; onChange: (r: KV[]) => void; label: string }) {
  const { t } = useTranslation();
  const update = (i: number, field: keyof KV, val: string | boolean) => {
    const next = rows.map((r, idx) => idx === i ? { ...r, [field]: val } : r);
    onChange(next);
  };
  const add = () => onChange([...rows, newKV()]);
  const remove = (i: number) => onChange(rows.filter((_, idx) => idx !== i));
  return (
    <div className="kv-editor">
      <div className="kv-header">
        <span className="kv-label">{label}</span>
        <button className="btn-ghost kv-add" onClick={add}><Plus size={11} /> {t("apiTest.add")}</button>
      </div>
      {rows.map((row, i) => (
        <div key={i} className="kv-row">
          <input type="checkbox" checked={row.enabled} onChange={(e) => update(i, "enabled", e.target.checked)} className="kv-check" />
          <input className="kv-input" placeholder="Key" value={row.key} onChange={(e) => update(i, "key", e.target.value)} />
          <input className="kv-input" placeholder="Value" value={row.value} onChange={(e) => update(i, "value", e.target.value)} />
          <button className="btn-icon-sm" onClick={() => remove(i)}><Trash2 size={10} /></button>
        </div>
      ))}
    </div>
  );
}

function prettifyJson(text: string): string {
  try { return JSON.stringify(JSON.parse(text), null, 2); } catch { return text; }
}

function buildUrl(base: string, params: KV[]): string {
  const active = params.filter((p) => p.enabled && p.key.trim());
  if (!active.length) return base;
  const qs = active.map((p) => `${encodeURIComponent(p.key)}=${encodeURIComponent(p.value)}`).join("&");
  return base.includes("?") ? `${base}&${qs}` : `${base}?${qs}`;
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
}

export default function ApiTestPanel() {
  const { t } = useTranslation();
  const [requests, setRequests] = useState<SavedRequest[]>([newRequest()]);
  const [activeId, setActiveId] = useState<string>(requests[0].id);
  const [globals, setGlobals] = useState<GlobalVar[]>([{ key: "BASE_URL", value: "http://localhost:8080" }]);
  const [showGlobals, setShowGlobals] = useState(false);
  const [reqTab, setReqTab] = useState<ReqTab>("params");
  const [response, setResponse] = useState<ResponseData | null>(null);
  const [loading, setLoading] = useState(false);
  const [copied, setCopied] = useState(false);
  const [resTab, setResTab] = useState<"body" | "headers">("body");
  const abortRef = useRef<AbortController | null>(null);

  const active = requests.find((r) => r.id === activeId) ?? requests[0];

  const update = useCallback((patch: Partial<SavedRequest>) => {
    setRequests((prev) => prev.map((r) => r.id === activeId ? { ...r, ...patch } : r));
  }, [activeId]);

  const addRequest = () => {
    const nr = newRequest();
    setRequests((prev) => [...prev, nr]);
    setActiveId(nr.id);
    setResponse(null);
  };

  const deleteRequest = (id: string) => {
    setRequests((prev) => {
      const next = prev.filter((r) => r.id !== id);
      if (next.length === 0) {
        const nr = newRequest(); return [nr];
      }
      return next;
    });
    if (activeId === id) setActiveId(requests.find((r) => r.id !== id)?.id ?? requests[0].id);
  };

  const applyGlobals = (s: string): string => {
    return globals.reduce((acc, g) => acc.replace(new RegExp(`\\{\\{${g.key}\\}\\}`, "g"), g.value), s);
  };

  const sendRequest = useCallback(async () => {
    if (!active.url.trim()) return;
    if (abortRef.current) abortRef.current.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    setLoading(true); setResponse(null);
    const t0 = performance.now();
    try {
      const url = applyGlobals(buildUrl(active.url, active.params));
      const headers: Record<string, string> = {};
      active.headers.filter((h) => h.enabled && h.key).forEach((h) => {
        headers[h.key] = applyGlobals(h.value);
      });
      let body: BodyInit | undefined;
      if (active.method !== "GET" && active.method !== "HEAD") {
        if (active.bodyType === "json") {
          body = applyGlobals(active.bodyRaw);
          headers["Content-Type"] = headers["Content-Type"] ?? "application/json";
        } else if (active.bodyType === "text") {
          body = applyGlobals(active.bodyRaw);
        } else if (active.bodyType === "form") {
          const fd = new FormData();
          active.bodyForm.filter((f) => f.enabled && f.key).forEach((f) => fd.append(f.key, f.value));
          body = fd;
        }
      }
      const res = await fetch(url, { method: active.method, headers, body, signal: ctrl.signal });
      const elapsed = Math.round(performance.now() - t0);
      const text = await res.text();
      const resHeaders: Record<string, string> = {};
      res.headers.forEach((v, k) => { resHeaders[k] = v; });
      setResponse({
        status: res.status, statusText: res.statusText,
        time: elapsed, size: formatSize(new TextEncoder().encode(text).length),
        headers: resHeaders,
        body: prettifyJson(text),
      });
    } catch (e: unknown) {
      if ((e as Error).name !== "AbortError") {
        setResponse({ status: 0, statusText: "", time: 0, size: "0 B", headers: {}, body: "", error: String(e) });
      }
    } finally {
      setLoading(false);
    }
  }, [active, globals]);

  const copyBody = () => {
    if (response?.body) { navigator.clipboard.writeText(response.body); setCopied(true); setTimeout(() => setCopied(false), 1500); }
  };

  const statusColor = (s: number) => s >= 500 ? "#e06c75" : s >= 400 ? "#e5c07b" : s >= 300 ? "#61afef" : s >= 200 ? "#98c379" : "#abb2bf";

  return (
    <div className="api-panel">
      {/* ── Left sidebar ── */}
      <div className="api-sidebar">
        <div className="api-sidebar-header">
          <FlaskConical size={12} className="api-sidebar-icon" />
          <span>{t("apiTest.title")}</span>
          <button className="btn-ghost-sm" onClick={addRequest} title={t("apiTest.newRequestTitle")}><Plus size={12} /></button>
        </div>
        <div className="api-request-list">
          {requests.map((r) => (
            <div
              key={r.id}
              className={`api-req-item${r.id === activeId ? " active" : ""}`}
              onClick={() => { setActiveId(r.id); setResponse(null); }}
            >
              <span className="api-req-method" style={{ color: METHOD_COLORS[r.method] }}>{r.method}</span>
              <span className="api-req-name">{r.name}</span>
              <button className="btn-icon-sm api-req-del" onClick={(e) => { e.stopPropagation(); deleteRequest(r.id); }}>
                <Trash2 size={9} />
              </button>
            </div>
          ))}
        </div>
        <div className="api-globals-section">
          <div className="api-globals-header" onClick={() => setShowGlobals(!showGlobals)}>
            {showGlobals ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
            <Settings size={10} />
            <span>{t("apiTest.globalVars")}</span>
          </div>
          {showGlobals && (
            <div className="api-globals-body">
              {globals.map((g, i) => (
                <div key={i} className="kv-row">
                  <input className="kv-input" placeholder="Key" value={g.key}
                    onChange={(e) => setGlobals((prev) => prev.map((v, j) => j === i ? { ...v, key: e.target.value } : v))} />
                  <input className="kv-input" placeholder="Value" value={g.value}
                    onChange={(e) => setGlobals((prev) => prev.map((v, j) => j === i ? { ...v, value: e.target.value } : v))} />
                  <button className="btn-icon-sm" onClick={() => setGlobals((prev) => prev.filter((_, j) => j !== i))}><Trash2 size={9} /></button>
                </div>
              ))}
              <button className="btn-ghost kv-add" onClick={() => setGlobals((prev) => [...prev, { key: "", value: "" }])}>
                <Plus size={11} /> {t("apiTest.add")}
              </button>
              <p className="api-globals-hint">{t("apiTest.globalHint")}</p>
            </div>
          )}
        </div>
      </div>

      {/* ── Main area ── */}
      <div className="api-main">
        {/* Name row */}
        <div className="api-name-row">
          <input
            className="api-name-input"
            value={active.name}
            onChange={(e) => update({ name: e.target.value })}
            placeholder={t("apiTest.requestName")}
          />
        </div>

        {/* URL bar */}
        <div className="api-url-bar">
          <select
            className="api-method-select"
            style={{ color: METHOD_COLORS[active.method] }}
            value={active.method}
            onChange={(e) => update({ method: e.target.value as HttpMethod })}
          >
            {METHODS.map((m) => <option key={m} value={m} style={{ color: METHOD_COLORS[m] }}>{m}</option>)}
          </select>
          <input
            className="api-url-input"
            value={active.url}
            onChange={(e) => update({ url: e.target.value })}
            placeholder={t("apiTest.urlPlaceholder")}
            onKeyDown={(e) => { if (e.key === "Enter") sendRequest(); }}
          />
          <button
            className="api-send-btn"
            onClick={loading ? () => abortRef.current?.abort() : sendRequest}
            disabled={!active.url.trim() && !loading}
          >
            {loading ? <><Loader2 size={13} className="spin" /> {t("apiTest.cancel")}</> : <><Send size={13} /> {t("apiTest.send")}</>}
          </button>
        </div>

        {/* Request tabs */}
        <div className="api-req-tabs">
          {(["params", "headers", "body", "auth"] as ReqTab[]).map((tab) => (
            <button key={tab} className={`api-tab-btn${reqTab === tab ? " active" : ""}`} onClick={() => setReqTab(tab)}>
              {tab === "params" ? t("apiTest.params") : tab === "headers" ? t("apiTest.headers") : tab === "body" ? t("apiTest.body") : t("apiTest.auth")}
            </button>
          ))}
        </div>
        <div className="api-req-body">
          {reqTab === "params" && (
            <KVEditor rows={active.params} onChange={(r) => update({ params: r })} label={t("apiTest.queryParams")} />
          )}
          {reqTab === "headers" && (
            <KVEditor rows={active.headers} onChange={(r) => update({ headers: r })} label={t("apiTest.reqHeaders")} />
          )}
          {reqTab === "body" && (
            <div className="api-body-section">
              <div className="api-body-type-row">
                {(["none", "json", "form", "text"] as BodyType[]).map((bt) => (
                  <label key={bt} className="api-body-radio">
                    <input type="radio" name="bodyType" checked={active.bodyType === bt} onChange={() => update({ bodyType: bt })} />
                    <span>{bt === "none" ? t("apiTest.bodyNone") : bt === "json" ? "JSON" : bt === "form" ? "Form Data" : t("apiTest.bodyText")}</span>
                  </label>
                ))}
              </div>
              {(active.bodyType === "json" || active.bodyType === "text") && (
                <textarea
                  className="api-body-raw"
                  value={active.bodyRaw}
                  onChange={(e) => update({ bodyRaw: e.target.value })}
                  placeholder={active.bodyType === "json" ? '{\n  "key": "value"\n}' : t("apiTest.bodyPlaceholder")}
                  spellCheck={false}
                />
              )}
              {active.bodyType === "form" && (
                <KVEditor rows={active.bodyForm} onChange={(r) => update({ bodyForm: r })} label="Form" />
              )}
            </div>
          )}
          {reqTab === "auth" && (
            <div className="api-auth-hint">
              <Globe size={20} strokeWidth={1} />
              <span>{t("apiTest.authHint")}</span>
            </div>
          )}
        </div>

        {/* Response */}
        <div className="api-response-section">
          <div className="api-response-header">
            <span className="api-response-title">Response</span>
            {response && !response.error && (
              <>
                <span className="api-status-badge" style={{ background: statusColor(response.status) + "22", color: statusColor(response.status) }}>
                  {response.status} {response.statusText}
                </span>
                <span className="api-meta-badge"><Clock size={10} /> {response.time} ms</span>
                <span className="api-meta-badge">{response.size}</span>
                <div style={{ flex: 1 }} />
                {(["body", "headers"] as const).map((rt) => (
                  <button key={rt} className={`api-tab-btn${resTab === rt ? " active" : ""}`} onClick={() => setResTab(rt)}>
                    {rt === "body" ? "Body" : "Headers"}
                  </button>
                ))}
                <button className="btn-ghost-sm" onClick={copyBody} title={t("apiTest.copy")}>
                  {copied ? <Check size={12} /> : <Copy size={12} />}
                </button>
              </>
            )}
          </div>
          <div className="api-response-body">
            {loading && <div className="api-loading"><Loader2 size={22} className="spin" /><span>Loading…</span></div>}
            {!loading && !response && (
              <div className="api-response-empty"><Send size={28} strokeWidth={1} /><span>Click Send to see response</span></div>
            )}
            {!loading && response?.error && (
              <div className="api-response-error"><AlertTriangle size={16} /><span>{response.error}</span></div>
            )}
            {!loading && response && !response.error && resTab === "body" && (
              <pre className="api-response-pre">{response.body || "(empty)"}</pre>
            )}
            {!loading && response && !response.error && resTab === "headers" && (
              <div className="api-response-headers">
                {Object.entries(response.headers).map(([k, v]) => (
                  <div key={k} className="api-resp-header-row">
                    <span className="api-resp-header-key">{k}</span>
                    <span className="api-resp-header-val">{v}</span>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
