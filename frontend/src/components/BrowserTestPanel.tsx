import React, { useState, useEffect, useCallback, useRef } from "react";
import { useTranslation } from "../i18n";
import {
  Play, Square, Plus, Trash2, ChevronUp, ChevronDown,
  FlaskConical, Loader2, AlertTriangle, ListOrdered, Code2, Save, Camera,
} from "lucide-react";
import { Events } from "@wailsio/runtime";
import MonacoEditor from "@monaco-editor/react";
import * as at from "../../bindings/github.com/linlurui/tiancan-ai-ide/backend/autotest/service";
import type { Script, Step } from "../../bindings/github.com/linlurui/tiancan-ai-ide/backend/autotest/models";

// 每种步骤要填哪些字段（顺序即渲染顺序）。
const STEP_FIELDS: Record<string, Array<{ key: keyof Step; label: string; ph: string; wide?: boolean }>> = {
  open: [{ key: "url", label: "URL", ph: "https://example.com", wide: true }],
  click: [{ key: "selector", label: "选择器", ph: "button#submit", wide: true }],
  fill: [
    { key: "selector", label: "选择器", ph: "input[name=\"q\"]", wide: true },
    { key: "text", label: "内容", ph: "要填写的文本", wide: true },
  ],
  assertText: [
    { key: "selector", label: "选择器", ph: "h1", wide: true },
    { key: "text", label: "应包含", ph: "期望文本", wide: true },
  ],
  assertTitle: [{ key: "text", label: "标题应包含", ph: "Example", wide: true }],
  screenshot: [{ key: "name", label: "文件名", ph: "home（可选）" }],
  wait: [
    { key: "ms", label: "毫秒", ph: "500" },
    { key: "selector", label: "或等待元素", ph: ".ready", wide: true },
  ],
};

const STEP_LABELS: Record<string, string> = {
  open: "打开页面",
  click: "点击元素",
  fill: "填写文本",
  assertText: "断言文本",
  assertTitle: "断言标题",
  screenshot: "截图",
  wait: "等待",
};

interface OutLine {
  kind: "info" | "ok" | "err" | "step" | "done";
  text: string;
  shot?: string;
}

function newScript(): Script {
  return {
    id: crypto.randomUUID(),
    name: "新测试",
    steps: [{ type: "open", url: "https://example.com" }],
  };
}

interface Props {
  projectRoot?: string | null;
}

export default function BrowserTestPanel({ projectRoot }: Props) {
  const { t } = useTranslation();
  const root = projectRoot ?? "";

  const [scripts, setScripts] = useState<Script[]>([]);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [jsonMode, setJsonMode] = useState(false);
  const [jsonText, setJsonText] = useState("");
  const [jsonError, setJsonError] = useState<string | null>(null);
  const [browserOK, setBrowserOK] = useState<boolean | null>(null);
  const [browserBin, setBrowserBin] = useState("");
  const [running, setRunning] = useState(false);
  const [output, setOutput] = useState<OutLine[]>([]);
  const [saving, setSaving] = useState(false);
  const runRef = useRef(false);

  const active = scripts.find((s) => s.id === activeId) ?? null;

  // ── 载入脚本列表 + 浏览器可用性 ──
  const reload = useCallback(async () => {
    try {
      const list = await at.ListScripts(root);
      setScripts(list ?? []);
      setActiveId((cur) => cur ?? list?.[0]?.id ?? null);
    } catch {
      setScripts([]);
    }
  }, [root]);

  useEffect(() => {
    void reload();
    at.BrowserReady().then((ok: boolean) => setBrowserOK(!!ok)).catch(() => setBrowserOK(false));
    at.BrowserBin().then((b: string) => setBrowserBin(b || "")).catch(() => {});
  }, [reload]);

  // ── 运行事件流 ──
  useEffect(() => {
    const off = Events.On("autotest:output", (evt: any) => {
      let data: any = evt;
      if (typeof evt === "string") {
        try { data = JSON.parse(evt); } catch { return; }
      }
      if (!data || typeof data !== "object") return;
      switch (data.type) {
        case "step":
          setOutput((p) => [...p, { kind: "step", text: `▶ 第 ${(data.index ?? 0) + 1} 步 · ${STEP_LABELS[data.step] || data.step}` }]);
          break;
        case "log":
          setOutput((p) => [...p, {
            kind: "ok",
            text: `✓ ${STEP_LABELS[data.step] || data.step}（${data.ms ?? 0}ms）`,
            shot: data.shot,
          }]);
          break;
        case "error":
          setOutput((p) => [...p, { kind: "err", text: `✗ ${data.line || "失败"}` }]);
          break;
        case "done":
          setOutput((p) => [...p, {
            kind: data.ok ? "done" : "err",
            text: data.ok ? `✓ ${data.line || "全部通过"}` : `✗ ${data.line || "失败"}`,
          }]);
          setRunning(false);
          runRef.current = false;
          break;
      }
    });
    return () => { if (off && typeof off === "function") off(); };
  }, []);

  // ── 编辑 ──
  const patchActive = (fn: (s: Script) => Script) => {
    setScripts((prev) => prev.map((s) => (s.id === activeId ? fn(s) : s)));
    setDirty(true);
  };

  const patchStep = (idx: number, fn: (st: Step) => Step) =>
    patchActive((s) => ({ ...s, steps: s.steps.map((st, i) => (i === idx ? fn(st) : st)) }));

  const addStep = (type: string) =>
    patchActive((s) => ({ ...s, steps: [...s.steps, { type } as Step] }));

  const removeStep = (idx: number) =>
    patchActive((s) => ({ ...s, steps: s.steps.filter((_, i) => i !== idx) }));

  const moveStep = (idx: number, dir: -1 | 1) =>
    patchActive((s) => {
      const j = idx + dir;
      if (j < 0 || j >= s.steps.length) return s;
      const steps = [...s.steps];
      [steps[idx], steps[j]] = [steps[j], steps[idx]];
      return { ...s, steps };
    });

  // ── JSON 源模式 ──
  const enterJson = () => {
    if (active) setJsonText(JSON.stringify(active, null, 2));
    setJsonError(null);
    setJsonMode(true);
  };

  const applyJson = () => {
    try {
      const sc = JSON.parse(jsonText) as Script;
      if (!sc.id || !Array.isArray(sc.steps)) throw new Error("缺少 id/steps");
      patchActive(() => sc);
      setJsonMode(false);
    } catch (e: any) {
      setJsonError(e?.message ?? String(e));
    }
  };

  // ── 保存 / 运行 ──
  const save = async (): Promise<Script | null> => {
    if (!active) return null;
    setSaving(true);
    try {
      await at.SaveScript(root, active);
      setDirty(false);
      return active;
    } catch (e: any) {
      setOutput((p) => [...p, { kind: "err", text: `✗ 保存失败: ${e?.message ?? e}` }]);
      return null;
    } finally {
      setSaving(false);
    }
  };

  const run = async () => {
    if (!active || running) return;
    const sc = await save(); // 后端从磁盘加载，必须先落盘
    if (!sc) return;
    setOutput([]);
    setRunning(true);
    runRef.current = true;
    try {
      await at.RunScript(root, sc.id);
      // 结果走事件流；RunScript 本身只负责启动。
    } catch (e: any) {
      setOutput((p) => [...p, { kind: "err", text: `✗ 启动失败: ${e?.message ?? e}` }]);
      setRunning(false);
      runRef.current = false;
    }
  };

  const stop = async () => {
    try { await at.StopRun(); } catch { /* 可能已结束 */ }
    setRunning(false);
    runRef.current = false;
  };

  const addScript = async () => {
    const sc = newScript();
    setScripts((prev) => [...prev, sc]);
    setActiveId(sc.id);
    setDirty(true);
  };

  const deleteScript = async (id: string) => {
    try { await at.DeleteScript(root, id); } catch { /* 文件可能本就不存在 */ }
    setScripts((prev) => {
      const next = prev.filter((s) => s.id !== id);
      if (activeId === id) setActiveId(next[0]?.id ?? null);
      return next;
    });
  };

  return (
    <div className="bt-panel">
      {/* ── 左栏：测试列表 ── */}
      <div className="bt-sidebar">
        <div className="bt-sidebar-header">
          <FlaskConical size={12} />
          <span>{t("browsertest.title")}</span>
          <button className="btn-ghost-sm" onClick={() => void addScript()} title={t("browsertest.new")}>
            <Plus size={12} />
          </button>
        </div>
        <div className="bt-script-list">
          {scripts.length === 0 && (
            <div className="bt-empty">{t("browsertest.emptyList")}</div>
          )}
          {scripts.map((s) => (
            <div key={s.id}
              className={`bt-script-item${s.id === activeId ? " active" : ""}`}
              onClick={() => { setActiveId(s.id); setJsonMode(false); }}>
              <FlaskConical size={11} className="bt-script-icon" />
              <span className="bt-script-name">{s.name || "(未命名)"}</span>
              <button className="btn-icon-sm bt-del-btn" title={t("browsertest.delete")}
                onClick={(e) => { e.stopPropagation(); void deleteScript(s.id); }}>
                <Trash2 size={9} />
              </button>
            </div>
          ))}
        </div>
      </div>

      {/* ── 主区 ── */}
      <div className="bt-main">
        {browserOK === false && (
          <div className="bt-nobrowser">
            <AlertTriangle size={13} />
            <span>{t("browsertest.noBrowser")}</span>
          </div>
        )}

        {!active ? (
          <div className="bt-empty-main">{t("browsertest.pickOrCreate")}</div>
        ) : (
          <>
            <div className="bt-toolbar">
              <input className="bt-name-input"
                value={active.name}
                onChange={(e) => patchActive((s) => ({ ...s, name: e.target.value }))}
                placeholder={t("browsertest.namePh")} />
              <span className="bt-storage" title={t("browsertest.storageTip")}>
                {root ? `.tiancan/tests/` : t("browsertest.globalStore")}
              </span>
              <div className="bt-toolbar-spacer" />
              <button className="bt-mode-btn" onClick={() => (jsonMode ? applyJson() : enterJson())}
                title={jsonMode ? t("browsertest.applyJson") : t("browsertest.jsonMode")}>
                {jsonMode ? <ListOrdered size={13} /> : <Code2 size={13} />}
                {jsonMode ? t("browsertest.stepMode") : t("browsertest.jsonMode")}
              </button>
              <button className="bt-save-btn" onClick={() => void save()} disabled={!dirty || saving}>
                {saving ? <Loader2 size={13} className="spin" /> : <Save size={13} />}
                {dirty ? t("browsertest.save") : t("browsertest.saved")}
              </button>
              {running ? (
                <button className="bt-stop-btn" onClick={() => void stop()}>
                  <Square size={12} /> {t("browsertest.stop")}
                </button>
              ) : (
                <button className="bt-run-btn" onClick={() => void run()} disabled={browserOK === false}>
                  <Play size={13} /> {t("browsertest.run")}
                </button>
              )}
            </div>

            {/* 步骤编辑 / JSON 编辑 */}
            {jsonMode ? (
              <div className="bt-json-wrap">
                <MonacoEditor
                  height="100%"
                  language="json"
                  theme="vs-dark"
                  value={jsonText}
                  onChange={(v) => setJsonText(v ?? "")}
                  options={{ minimap: { enabled: false }, fontSize: 12, scrollBeyondLastLine: false, wordWrap: "on" }}
                />
                {jsonError && <div className="bt-json-error">{jsonError}</div>}
              </div>
            ) : (
              <div className="bt-steps">
                {active.steps.length === 0 && (
                  <div className="bt-empty">{t("browsertest.noSteps")}</div>
                )}
                {active.steps.map((st, i) => (
                  <div className="bt-step" key={i}>
                    <div className="bt-step-grip">
                      <button className="btn-icon-sm" disabled={i === 0}
                        onClick={() => moveStep(i, -1)} title={t("browsertest.up")}>
                        <ChevronUp size={10} />
                      </button>
                      <button className="btn-icon-sm" disabled={i === active.steps.length - 1}
                        onClick={() => moveStep(i, 1)} title={t("browsertest.down")}>
                        <ChevronDown size={10} />
                      </button>
                    </div>
                    <span className="bt-step-no">{i + 1}</span>
                    <select className="bt-step-type" value={st.type}
                      onChange={(e) => patchStep(i, (s) => ({ type: e.target.value } as Step))}>
                      {Object.keys(STEP_FIELDS).map((tp) => (
                        <option key={tp} value={tp}>{STEP_LABELS[tp]}</option>
                      ))}
                    </select>
                    <div className="bt-step-fields">
                      {(STEP_FIELDS[st.type] ?? []).map((f) => (
                        <input key={f.key as string}
                          className={f.wide ? "bt-step-input wide" : "bt-step-input"}
                          placeholder={f.ph}
                          value={String(st[f.key] ?? "")}
                          onChange={(e) => {
                            const raw = e.target.value;
                            const val = f.key === "ms" ? (parseInt(raw, 10) || 0) : raw;
                            patchStep(i, (s) => ({ ...s, [f.key]: val } as Step));
                          }} />
                      ))}
                    </div>
                    <button className="btn-icon-sm bt-del-btn" onClick={() => removeStep(i)} title={t("browsertest.removeStep")}>
                      <Trash2 size={9} />
                    </button>
                  </div>
                ))}
                <div className="bt-add-row">
                  <select className="bt-add-select" defaultValue=""
                    onChange={(e) => { if (e.target.value) { addStep(e.target.value); e.target.value = ""; } }}>
                    <option value="" disabled>{t("browsertest.addStep")}</option>
                    {Object.keys(STEP_FIELDS).map((tp) => (
                      <option key={tp} value={tp}>{STEP_LABELS[tp]}</option>
                    ))}
                  </select>
                </div>
              </div>
            )}

            {/* 输出 */}
            <div className="bt-output">
              <div className="bt-output-header">
                <span>{t("browsertest.output")}</span>
                {output.length > 0 && (
                  <button className="btn-ghost-sm" onClick={() => setOutput([])}>{t("browsertest.clear")}</button>
                )}
              </div>
              <div className="bt-output-body">
                {output.length === 0 ? (
                  <span className="bt-output-empty">
                    {browserOK === false ? t("browsertest.needBrowser") : t("browsertest.runHint")}
                  </span>
                ) : (
                  output.map((l, i) => (
                    <div key={i} className={`bt-out-line ${l.kind}`}>
                      <span>{l.text}</span>
                      {l.shot && (
                        <span className="bt-out-shot"><Camera size={10} /> {l.shot}</span>
                      )}
                    </div>
                  ))
                )}
              </div>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
