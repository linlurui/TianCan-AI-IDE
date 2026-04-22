import React, { useState, useEffect, useCallback } from "react";
import { useTranslation } from "../i18n";
import { Play, Square, Plus, Trash2, ChevronRight, ChevronDown, Code2, Loader2, Download } from "lucide-react";
import { Events } from "@wailsio/runtime";
import * as pwAPI from "../../bindings/github.com/rocky233/tiancan-ai-ide/backend/playwright/service";

interface Script { id: string; name: string; code: string }

const STARTER = `// Playwright script example (runs in Node.js environment)
const { chromium } = require('playwright');

(async () => {
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage();
  await page.goto('https://example.com');
  const title = await page.title();
  console.log('Title:', title);
  await browser.close();
})();
`;

const SNIPPETS = [
  { labelKey: "playwright.openPage", code: "await page.goto('https://example.com');" },
  { labelKey: "playwright.clickElement", code: "await page.click('selector');" },
  { labelKey: "playwright.fillText", code: "await page.fill('input[name=\"q\"]', 'keyword');" },
  { labelKey: "playwright.screenshot", code: "await page.screenshot({ path: 'screenshot.png' });" },
  { labelKey: "playwright.waitElement", code: "await page.waitForSelector('.result');" },
  { labelKey: "playwright.getText", code: "const text = await page.textContent('h1');" },
  { labelKey: "playwright.assertTitle", code: "const title = await page.title();\nconsole.assert(title.includes('expected'), 'title mismatch');" },
];

function newScript(): Script {
  return { id: crypto.randomUUID(), name: "New Script", code: STARTER };
}

export default function PlaywrightPanel() {
  const { t } = useTranslation();
  const [scripts, setScripts] = useState<Script[]>([newScript()]);
  const [activeId, setActiveId] = useState(scripts[0].id);
  const [running, setRunning] = useState(false);
  const [output, setOutput] = useState<string[]>([]);
  const [showSnippets, setShowSnippets] = useState(true);
  const [pwReady, setPwReady] = useState(false);
  const [installing, setInstalling] = useState(false);

  const active = scripts.find((s) => s.id === activeId) ?? scripts[0];

  // Check Playwright readiness on mount
  useEffect(() => {
    pwAPI.IsPlaywrightReady().then((ready) => setPwReady(!!ready)).catch(() => setPwReady(false));
  }, []);

  // Listen for streaming output events
  useEffect(() => {
    const off = Events.On("playwright:output", (evt: any) => {
      try {
        const data = typeof evt === "string" ? JSON.parse(evt) : evt;
        if (data.type === "stdout") {
          setOutput((prev) => [...prev, data.line]);
        } else if (data.type === "stderr") {
          setOutput((prev) => [...prev, `⚠ ${data.line}`]);
        } else if (data.type === "done") {
          const result = data.result;
          if (result) {
            setOutput((prev) => [
              ...prev,
              `─── Finished (exit: ${result.exitCode}, ${result.durationMs}ms) ───`,
              ...(result.error ? [`❌ ${result.error}`] : []),
            ]);
          }
          setRunning(false);
        }
      } catch { /* ignore parse errors */ }
    });
    return () => { if (off && typeof off === "function") off(); };
  }, []);

  const updateCode = (code: string) =>
    setScripts((prev) => prev.map((s) => s.id === activeId ? { ...s, code } : s));

  const updateName = (name: string) =>
    setScripts((prev) => prev.map((s) => s.id === activeId ? { ...s, name } : s));

  const addScript = () => {
    const ns = newScript();
    setScripts((prev) => [...prev, ns]);
    setActiveId(ns.id);
    setOutput([]);
  };

  const deleteScript = (id: string) => {
    setScripts((prev) => {
      const next = prev.filter((s) => s.id !== id);
      if (!next.length) { const ns = newScript(); return [ns]; }
      return next;
    });
    if (activeId === id)
      setActiveId(scripts.find((s) => s.id !== id)?.id ?? scripts[0].id);
  };

  const insertSnippet = (code: string) => {
    updateCode(active.code + (active.code.endsWith("\n") ? "" : "\n") + code + "\n");
  };

  const handleInstall = async () => {
    setInstalling(true);
    try {
      const logs = await pwAPI.InstallPlaywright();
      setOutput((prev) => [...prev, ...(logs || [])]);
      setPwReady(true);
    } catch (e: any) {
      setOutput((prev) => [...prev, `❌ Install failed: ${e?.message ?? e}`]);
      setPwReady(false);
    }
    setInstalling(false);
  };

  const runScript = async () => {
    // Save script to backend first
    try {
      await pwAPI.SaveScript({ id: active.id, name: active.name, code: active.code });
    } catch (e: any) {
      setOutput([`❌ Save failed: ${e?.message ?? e}`]);
      return;
    }

    setRunning(true);
    setOutput([`▶ Running: ${active.name}…`]);

    try {
      await pwAPI.RunScript(active.id);
    } catch (e: any) {
      setOutput((prev) => [...prev, `❌ Run failed: ${e?.message ?? e}`]);
      setRunning(false);
    }
  };

  const stopScript = async () => {
    try {
      await pwAPI.StopScript(active.id);
      setOutput((prev) => [...prev, "⏹ Stopped."]);
    } catch { /* ignore */ }
    setRunning(false);
  };

  return (
    <div className="pw-panel">
      {/* ── Left sidebar ── */}
      <div className="pw-sidebar">
        <div className="pw-sidebar-header">
          <Code2 size={12} />
          <span>{t("playwright.title")}</span>
          <button className="btn-ghost-sm" onClick={addScript}><Plus size={12} /></button>
        </div>
        <div className="pw-script-list">
          {scripts.map((s) => (
            <div key={s.id} className={`pw-script-item${s.id === activeId ? " active" : ""}`}
              onClick={() => { setActiveId(s.id); setOutput([]); }}>
              <Code2 size={11} className="pw-script-icon" />
              <span className="pw-script-name">{s.name}</span>
              <button className="btn-icon-sm pw-del-btn" onClick={(e) => { e.stopPropagation(); deleteScript(s.id); }}>
                <Trash2 size={9} />
              </button>
            </div>
          ))}
        </div>

        <div className="pw-snippets-section">
          <div className="pw-snippets-header" onClick={() => setShowSnippets(!showSnippets)}>
            {showSnippets ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
            <span>{t("playwright.snippets")}</span>
          </div>
          {showSnippets && (
            <div className="pw-snippets-list">
              {SNIPPETS.map((s) => (
                <div key={s.labelKey} className="pw-snippet-item" onClick={() => insertSnippet(s.code)}>
                  <Plus size={9} />
                  <span>{t(s.labelKey)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* ── Main area ── */}
      <div className="pw-main">
        <div className="pw-toolbar">
          <input className="pw-name-input" value={active.name}
            onChange={(e) => updateName(e.target.value)} placeholder={t("playwright.scriptName")} />
          {!pwReady && (
            <button className="pw-install-btn" onClick={handleInstall} disabled={installing}>
              {installing ? <><Loader2 size={13} className="spin" /> Installing…</> : <><Download size={13} /> Install Playwright</>}
            </button>
          )}
          <button className="pw-run-btn" onClick={runScript} disabled={running || !pwReady}>
            {running ? <><Loader2 size={13} className="spin" /> Running…</> : <><Play size={13} /> Run</>}
          </button>
          {running && (
            <button className="pw-stop-btn" onClick={stopScript}>
              <Square size={12} /> Stop
            </button>
          )}
        </div>

        <div className="pw-editor-wrap">
          <textarea
            className="pw-code-editor"
            value={active.code}
            onChange={(e) => updateCode(e.target.value)}
            spellCheck={false}
          />
        </div>

        <div className="pw-output-section">
          <div className="pw-output-header">
            <span>Output</span>
            <button className="btn-ghost-sm" onClick={() => setOutput([])}>Clear</button>
          </div>
          <div className="pw-output-body">
            {output.length === 0
              ? <span className="pw-output-empty">{pwReady ? "Click Run to see output" : "Install Playwright first"}</span>
              : output.map((line, i) => <div key={i} className="pw-output-line">{line}</div>)
            }
          </div>
        </div>
      </div>
    </div>
  );
}
