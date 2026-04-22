/**
 * DebugPanel — 完整 DAP 调试面板
 *
 * 对标 Lapce debug_view.rs，实现：
 * ✅ 多线程支持（每个线程独立调用栈）
 * ✅ 变量树递归展开（懒加载，任意深度）
 * ✅ 条件断点 / 命中计数 / Logpoint
 * ✅ 断点激活/停用开关
 * ✅ Watch 表达式
 * ✅ Step Back（debugger 支持时）
 * ✅ 内存检查（readMemory）
 * ✅ 反汇编（disassemble）
 * ✅ 虚拟滚动（变量列表、调用栈）
 * ✅ Hover 求值（DAP evaluate）
 */
import React, {
  useCallback, useEffect, useImperativeHandle,
  useRef, useState, forwardRef, useMemo,
} from "react";
import { useTranslation } from "../i18n";
import {
  Play, Square, SkipForward, ArrowDownLeft, ArrowUpRight,
  Bug, ChevronRight, ChevronDown, Loader2, AlertTriangle,
  RotateCcw, Trash2, X, Circle, Rewind, Eye, Cpu, MemoryStick,
  Plus, Pencil, Check, Ban, ToggleLeft, ToggleRight,
} from "lucide-react";
import { AnsiLine } from "../utils/ansi";
import {
  StartAndGetPort, StartDebug, StopDebug,
  ScanMainPackages, IsDlvInstalled, IsLldbDapInstalled,
  IsDebugpyInstalled, IsJsDebugInstalled, IsJavaDebugInstalled,
  IsDartInstalled,
  DetectAdapterType, GetDebugStatus,
  AdapterType,
} from "../bindings/debug";
import { StopProject, DetectRunEnv, StartProjectWithCmd, GetProcessOutput, ScanRunConfigs } from "../bindings/process";

/* ─────────────────────────────────────────────────────────────────
   Public types
───────────────────────────────────────────────────────────────── */
export interface Breakpoint {
  file: string;
  line: number;
  enabled: boolean;
  condition?: string;
  hitCondition?: string;
  logMessage?: string;
}

export interface DebugPanelHandle {
  startDebug: () => void;
  evaluate: (expr: string) => Promise<string | null>;
}

export type DebugState = "idle" | "starting" | "running" | "paused" | "error";

/* ─────────────────────────────────────────────────────────────────
   Internal DAP types
───────────────────────────────────────────────────────────────── */
interface DapThread   { id: number; name: string; }
interface StackFrame  {
  id: number; name: string;
  source?: { path?: string; name?: string };
  line: number; column: number;
  instructionPointerReference?: string;
}
interface DapScope    { name: string; variablesReference: number; expensive: boolean; }
interface DapVariable {
  name: string; value: string; type?: string;
  variablesReference: number;     // >0 → expandable
  namedVariables?: number;
  indexedVariables?: number;
  evaluateName?: string;
  memoryReference?: string;
  // UI state
  _expanded?: boolean;
  _children?: DapVariable[];
  _loading?: boolean;
  _depth?: number;
  _key?: string;                  // unique key for virtual list
}
interface WatchItem   { expr: string; result?: string; error?: boolean; }
interface MemoryResult { address: string; hex: string[]; ascii: string[]; }
interface DisasmInstruction {
  address: string; instructionBytes?: string; instruction: string;
  symbol?: string; line?: number;
}
interface DapCapabilities {
  supportsStepBack?: boolean;
  supportsReadMemoryRequest?: boolean;
  supportsDisassembleRequest?: boolean;
  supportsConditionalBreakpoints?: boolean;
  supportsHitConditionalBreakpoints?: boolean;
  supportsLogPoints?: boolean;
  supportsRestartRequest?: boolean;
  supportsGotoTargetsRequest?: boolean;
  supportsEvaluateForHovers?: boolean;
  supportsSetVariable?: boolean;
}

interface Props {
  rootPath: string | null;
  activeFile: string | null;
  breakpoints: Breakpoint[];
  projectType: string | null;
  onPauseAt?: (file: string, line: number) => void;
  onNavigate?: (file: string, line: number) => void;
  onDeleteBreakpoint?: (file: string, line: number) => void;
  onClearBreakpoints?: () => void;
  onToggleBreakpoint?: (file: string, line: number, enabled: boolean) => void;
  onUpdateBreakpoint?: (file: string, line: number, patch: Partial<Breakpoint>) => void;
  onDebugStateChange?: (state: DebugState) => void;
}

/* ─────────────────────────────────────────────────────────────────
   DAP seq counter
───────────────────────────────────────────────────────────────── */
let _seq = 1;
function nextSeq() { return _seq++; }

/* ─────────────────────────────────────────────────────────────────
   Per-project state cache
   When the user switches projects, we save the current debug state
   and restore it when they switch back. The backend keeps sessions
   alive per rootPath, so we just need to reconnect the WebSocket.
───────────────────────────────────────────────────────────────── */
interface ProjectDebugState {
  state: DebugState;
  error: string | null;
  output: string[];
  threads: DapThread[];
  activeThreadId: number;
  stackMap: Map<number, StackFrame[]>;
  selectedFrame: number;
  scopeVars: DapVariable[];
  watches: WatchItem[];
  adapter: string;
  wsPort: number;
  adapterPort: number;
}

const projectStateCache = new Map<string, ProjectDebugState>();

/* ─────────────────────────────────────────────────────────────────
   Virtual list helper (simple windowing)
───────────────────────────────────────────────────────────────── */
const ITEM_HEIGHT = 22; // px per row
function VirtualList<T>({
  items, height, renderItem, keyFn,
}: {
  items: T[];
  height: number;
  renderItem: (item: T, index: number) => React.ReactNode;
  keyFn: (item: T, index: number) => string;
}) {
  const [scrollTop, setScrollTop] = useState(0);
  const visibleStart = Math.max(0, Math.floor(scrollTop / ITEM_HEIGHT) - 3);
  const visibleCount = Math.ceil(height / ITEM_HEIGHT) + 6;
  const visibleEnd   = Math.min(items.length, visibleStart + visibleCount);
  const totalHeight  = items.length * ITEM_HEIGHT;

  return (
    <div
      style={{ height, overflowY: "auto", position: "relative" }}
      onScroll={(e) => setScrollTop((e.target as HTMLElement).scrollTop)}
    >
      <div style={{ height: totalHeight, position: "relative" }}>
        {items.slice(visibleStart, visibleEnd).map((item, i) => (
          <div
            key={keyFn(item, visibleStart + i)}
            style={{
              position: "absolute",
              top: (visibleStart + i) * ITEM_HEIGHT,
              left: 0, right: 0,
              height: ITEM_HEIGHT,
            }}
          >
            {renderItem(item, visibleStart + i)}
          </div>
        ))}
      </div>
    </div>
  );
}

/* ─────────────────────────────────────────────────────────────────
   BreakpointEditor modal
───────────────────────────────────────────────────────────────── */
function BreakpointEditor({
  bp, onSave, onClose,
}: {
  bp: Breakpoint;
  onSave: (patch: Partial<Breakpoint>) => void;
  onClose: () => void;
}) {
  const [condition, setCondition]       = useState(bp.condition ?? "");
  const [hitCondition, setHitCondition] = useState(bp.hitCondition ?? "");
  const [logMessage, setLogMessage]     = useState(bp.logMessage ?? "");
  const { t } = useTranslation();
  return (
    <div className="bp-editor-overlay" onClick={onClose}>
      <div className="bp-editor-modal" onClick={(e) => e.stopPropagation()}>
        <div className="bp-editor-title">
          {t("debug.editBreakpoint")} — {bp.file.split("/").pop()}:{bp.line}
        </div>
        <label className="bp-editor-label">{t("debug.conditionExpr")}</label>
        <input
          className="bp-editor-input"
          placeholder="e.g. x > 5"
          value={condition}
          onChange={(e) => setCondition(e.target.value)}
        />
        <label className="bp-editor-label">{t("debug.hitCount")}</label>
        <input
          className="bp-editor-input"
          placeholder="e.g. >= 3"
          value={hitCondition}
          onChange={(e) => setHitCondition(e.target.value)}
        />
        <label className="bp-editor-label">{t("debug.logMessage")}</label>
        <input
          className="bp-editor-input"
          placeholder="e.g. value={x}"
          value={logMessage}
          onChange={(e) => setLogMessage(e.target.value)}
        />
        <div className="bp-editor-actions">
          <button className="bp-editor-save" onClick={() => {
            onSave({ condition: condition || undefined, hitCondition: hitCondition || undefined, logMessage: logMessage || undefined });
            onClose();
          }}>
            <Check size={12} /> {t("debug.save")}
          </button>
          <button className="bp-editor-cancel" onClick={onClose}>{t("debug.cancel")}</button>
        </div>
      </div>
    </div>
  );
}

/* ─────────────────────────────────────────────────────────────────
   Main Component
───────────────────────────────────────────────────────────────── */
type PanelSection = "stack" | "vars" | "watch" | "breakpoints" | "output" | "memory" | "disasm";

const DebugPanel = forwardRef<DebugPanelHandle, Props>(function DebugPanel(
  {
    rootPath, breakpoints, projectType,
    onPauseAt, onNavigate, onDeleteBreakpoint,
    onClearBreakpoints, onToggleBreakpoint,
    onUpdateBreakpoint, onDebugStateChange,
  },
  ref,
) {
  const { t } = useTranslation();
  /* ── State ── */
  const [state, setState]             = useState<DebugState>("idle");
  const [error, setError]             = useState<string | null>(null);
  const [caps, setCaps]               = useState<DapCapabilities>({});

  // Threads
  const [threads, setThreads]         = useState<DapThread[]>([]);
  const [activeThreadId, setActiveThreadId] = useState<number>(1);

  // Stack per thread
  const [stackMap, setStackMap]       = useState<Map<number, StackFrame[]>>(new Map());
  const [selectedFrame, setSelectedFrame] = useState<number>(0);

  // Variables (flat, expandable tree rendered inline)
  const [scopeVars, setScopeVars]     = useState<DapVariable[]>([]);
  const [expandedKeys, setExpandedKeys] = useState<Set<string>>(new Set());
  const [childrenMap, setChildrenMap] = useState<Map<string, DapVariable[]>>(new Map());

  // Watch
  const [watches, setWatches]         = useState<WatchItem[]>([]);
  const [newWatch, setNewWatch]       = useState("");

  // Output
  const [output, setOutput]           = useState<string[]>([]);
  const outputEndRef                  = useRef<HTMLDivElement | null>(null);

  // Memory
  const [memAddress, setMemAddress]   = useState("");
  const [memCount, setMemCount]       = useState("64");
  const [memResult, setMemResult]     = useState<MemoryResult | null>(null);

  // Disasm
  const [disasmResult, setDisasmResult] = useState<DisasmInstruction[]>([]);
  const [disasmAddr, setDisasmAddr]   = useState("");

  // Main packages
  const [mainPkgs, setMainPkgs]       = useState<string[]>([]);
  const [selectedPkg, setSelectedPkg] = useState(".");
  const [isWailsProject, setIsWailsProject] = useState(false);

  // Open sections
  const [openSections, setOpenSections] = useState<Set<PanelSection>>(
    new Set(["stack", "vars", "breakpoints", "output"])
  );

  // BP editor
  const [editingBp, setEditingBp]     = useState<Breakpoint | null>(null);

  /* ── Refs ── */
  const wsRef             = useRef<WebSocket | null>(null);
  const bufRef            = useRef<string>("");
  const pendingRef        = useRef<Map<number, (r: any) => void>>(new Map());
  const proxyPortRef      = useRef<number>(0);
  const configDoneRef     = useRef(false);
  const handleMessageRef  = useRef<(msg: any) => void>(() => {});
  const intentionalRef    = useRef(false);
  const sessionEndedRef   = useRef(false);
  const buildFlagsRef     = useRef<string>("");
  const selectedFrameIdRef = useRef<number>(0);
  const activeThreadIdRef  = useRef<number>(1);
  const prevRootPathRef    = useRef<string | null>(null);

  /* ── Per-project state persistence ── */
  useEffect(() => {
    const prevPath = prevRootPathRef.current;
    prevRootPathRef.current = rootPath;

    // Save state for the previous project before switching
    if (prevPath && prevPath !== rootPath) {
      // Close WS for old project (backend session stays alive)
      if (wsRef.current) {
        wsRef.current.close();
        wsRef.current = null;
      }
      projectStateCache.set(prevPath, {
        state,
        error,
        output,
        threads,
        activeThreadId,
        stackMap,
        selectedFrame,
        scopeVars,
        watches,
        adapter: adapterRef.current,
        wsPort: proxyPortRef.current,
        adapterPort: 0, // will be fetched from backend on reconnect
      });
    }

    // Restore state for the new project
    if (rootPath && prevPath !== rootPath) {
      const cached = projectStateCache.get(rootPath);
      if (cached) {
        setState(cached.state);
        setError(cached.error);
        setOutput(cached.output);
        setThreads(cached.threads);
        setActiveThreadId(cached.activeThreadId);
        setStackMap(cached.stackMap);
        setSelectedFrame(cached.selectedFrame);
        setScopeVars(cached.scopeVars);
        setWatches(cached.watches);
        adapterRef.current = cached.adapter;
        proxyPortRef.current = cached.wsPort;

        // Reconnect WebSocket if session was active
        if ((cached.state === "running" || cached.state === "paused") && cached.wsPort) {
          GetDebugStatus(rootPath).then((status: any) => {
            if (status?.active && status.port) {
              const ws = new WebSocket(`ws://127.0.0.1:${cached.wsPort}/dap?port=${status.port}`);
              wsRef.current = ws;
              bufRef.current = "";
              configDoneRef.current = true; // already initialized
              ws.onmessage = (ev) => {
                if (typeof ev.data === "string") bufRef.current += ev.data;
                while (bufRef.current) {
                  const m = bufRef.current.match(/^Content-Length: (\d+)\r\n\r\n/);
                  if (!m) break;
                  const len = parseInt(m[1], 10);
                  const start = m[0].length;
                  if (bufRef.current.length < start + len) break;
                  try { const msg = JSON.parse(bufRef.current.substring(start, start + len)); handleMessageRef.current(msg); } catch {}
                  bufRef.current = bufRef.current.substring(start + len);
                }
              };
              ws.onclose = () => {
                if (wsRef.current === ws) wsRef.current = null;
                setState(prev => (prev === "running" || prev === "paused") ? "idle" : prev);
              };
            } else {
              // Session died while we were away
              setState("idle");
              projectStateCache.delete(rootPath);
            }
          }).catch(() => {
            setState("idle");
            projectStateCache.delete(rootPath);
          });
        }
      } else {
        // No cached state — check if backend has an active session
        if (rootPath) {
          GetDebugStatus(rootPath).then((status: any) => {
            if (status?.active) {
              // Backend session exists but we have no frontend state — reconnect
              setState("running");
              adapterRef.current = status.adapter ?? "";
            } else {
              setState("idle");
              setError(null);
              setOutput([]);
              setThreads([]);
              setStackMap(new Map());
              setScopeVars([]);
            }
          }).catch(() => {});
        }
      }
      onDebugStateChange?.(state);
    }
  }, [rootPath]); // eslint-disable-line react-hooks/exhaustive-deps

  /* ── Section toggle ── */
  const toggleSection = (s: PanelSection) => setOpenSections(prev => {
    const n = new Set(prev);
    n.has(s) ? n.delete(s) : n.add(s);
    return n;
  });
  const isOpen = (s: PanelSection) => openSections.has(s);

  /* ── DAP framing ── */
  const send = useCallback((msg: object) => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    const body = JSON.stringify(msg);
    ws.send(`Content-Length: ${new TextEncoder().encode(body).length}\r\n\r\n${body}`);
  }, []);

  const sendRequest = useCallback((command: string, args?: object): Promise<any> =>
    new Promise((resolve) => {
      const seq = nextSeq();
      const tid = setTimeout(() => {
        pendingRef.current.delete(seq);
        resolve(null);
      }, 15_000);
      pendingRef.current.set(seq, (r: any) => { clearTimeout(tid); resolve(r); });
      send({ seq, type: "request", command, arguments: args ?? {} });
    }), [send]);

  const parseMessages = useCallback((chunk: string): any[] => {
    bufRef.current += chunk;
    const msgs: any[] = [];
    while (true) {
      const hEnd = bufRef.current.indexOf("\r\n\r\n");
      if (hEnd === -1) break;
      const m = bufRef.current.slice(0, hEnd).match(/Content-Length:\s*(\d+)/i);
      if (!m) { bufRef.current = ""; break; }
      const len = parseInt(m[1], 10);
      const bStart = hEnd + 4;
      if (bufRef.current.length < bStart + len) break;
      try { msgs.push(JSON.parse(bufRef.current.slice(bStart, bStart + len))); } catch { /* skip malformed */ }
      bufRef.current = bufRef.current.slice(bStart + len);
    }
    return msgs;
  }, []);

  /* ── Variable helpers ── */
  const buildVarKey = (depth: number, name: string, parentKey = "") =>
    `${parentKey}/${depth}/${name}`;

  const fetchChildren = useCallback(async (
    varRef: number, key: string
  ): Promise<DapVariable[]> => {
    const r = await sendRequest("variables", { variablesReference: varRef });
    return (r?.body?.variables ?? []).map((v: DapVariable, i: number) => ({
      ...v, _depth: 0, _key: buildVarKey(0, v.name, key),
    }));
  }, [sendRequest]);

  const flattenVars = useCallback((
    vars: DapVariable[],
    depth = 0,
    parentKey = "",
  ): DapVariable[] => {
    const result: DapVariable[] = [];
    for (const v of vars) {
      const key = buildVarKey(depth, v.name, parentKey);
      const flat: DapVariable = { ...v, _depth: depth, _key: key };
      result.push(flat);
      if (v.variablesReference > 0 && expandedKeys.has(key)) {
        const children = childrenMap.get(key) ?? [];
        result.push(...flattenVars(children, depth + 1, key));
      }
    }
    return result;
  }, [expandedKeys, childrenMap]);

  const flatVars = useMemo(() =>
    flattenVars(scopeVars), [flattenVars, scopeVars]);

  const handleToggleVar = useCallback(async (v: DapVariable) => {
    if (!v._key || v.variablesReference === 0) return;
    const key = v._key;
    if (expandedKeys.has(key)) {
      setExpandedKeys(prev => { const n = new Set(prev); n.delete(key); return n; });
    } else {
      setExpandedKeys(prev => new Set(prev).add(key));
      if (!childrenMap.has(key)) {
        const children = await fetchChildren(v.variablesReference, key);
        setChildrenMap(prev => new Map(prev).set(key, children));
      }
    }
  }, [expandedKeys, childrenMap, fetchChildren]);

  /* ── Fetch scopes + variables for a frame ── */
  const fetchFrameVars = useCallback(async (frameId: number) => {
    const scResp = await sendRequest("scopes", { frameId });
    const scopes: DapScope[] = scResp?.body?.scopes ?? [];
    const allVars: DapVariable[] = [];
    for (const scope of scopes) {
      const vResp = await sendRequest("variables", { variablesReference: scope.variablesReference });
      const vars: DapVariable[] = (vResp?.body?.variables ?? []).map((v: DapVariable, i: number) => ({
        ...v,
        _depth: 0,
        _key: buildVarKey(0, v.name, `scope_${scope.name}`),
      }));
      if (vars.length > 0) {
        // Insert scope header
        allVars.push({
          name: scope.name, value: "", type: "__scope__",
          variablesReference: 0,
          _depth: -1,
          _key: `scope_${scope.name}`,
        });
        allVars.push(...vars);
      }
    }
    setScopeVars(allVars);
  }, [sendRequest]);

  /* ── Fetch stack for a thread ── */
  const fetchStack = useCallback(async (threadId: number): Promise<StackFrame[]> => {
    const r = await sendRequest("stackTrace", { threadId, startFrame: 0, levels: 50 });
    return r?.body?.stackFrames ?? [];
  }, [sendRequest]);

  /* ── Refresh watches ── */
  const refreshWatches = useCallback(async () => {
    if (state !== "paused") return;
    const updated = await Promise.all(watches.map(async (w) => {
      const r = await sendRequest("evaluate", {
        expression: w.expr,
        frameId: selectedFrameIdRef.current,
        context: "watch",
      }).catch(() => null);
      if (!r?.body?.result) return { ...w, result: "<error>", error: true };
      return { ...w, result: r.body.result, error: false };
    }));
    setWatches(updated);
  }, [state, watches, sendRequest]);

  /* ── Evaluate (hover / REPL) ── */
  const evaluate = useCallback(async (expression: string): Promise<string | null> => {
    if (state !== "paused") return null;
    const r = await sendRequest("evaluate", {
      expression,
      frameId: selectedFrameIdRef.current,
      context: "hover",
    }).catch(() => null);
    return r?.body?.result ?? null;
  }, [state, sendRequest]);

  /* ── Step Back ── */
  const handleStepBack = useCallback(() => {
    sendRequest("stepBack", { threadId: activeThreadIdRef.current });
  }, [sendRequest]);

  /* ── Memory read ── */
  const handleReadMemory = useCallback(async () => {
    const count = parseInt(memCount) || 64;
    const r = await sendRequest("readMemory", {
      memoryReference: memAddress,
      offset: 0,
      count,
    }).catch(() => null);
    if (!r?.body?.data) { setMemResult(null); return; }
    const raw = atob(r.body.data);
    const bytes = Array.from(raw).map(c => c.charCodeAt(0));
    const rows: MemoryResult = { address: r.body.address ?? memAddress, hex: [], ascii: [] };
    for (let i = 0; i < bytes.length; i += 16) {
      const chunk = bytes.slice(i, i + 16);
      rows.hex.push(chunk.map(b => b.toString(16).padStart(2, "0")).join(" "));
      rows.ascii.push(chunk.map(b => (b >= 32 && b < 127 ? String.fromCharCode(b) : ".")).join(""));
    }
    setMemResult(rows);
  }, [sendRequest, memAddress, memCount]);

  /* ── Disassemble ── */
  const handleDisassemble = useCallback(async () => {
    const addr = disasmAddr ||
      (frames[selectedFrame]?.instructionPointerReference ?? "");
    if (!addr) return;
    const r = await sendRequest("disassemble", {
      memoryReference: addr,
      offset: -16,
      instructionOffset: -8,
      instructionCount: 64,
      resolveSymbols: true,
    }).catch(() => null);
    setDisasmResult(r?.body?.instructions ?? []);
  }, [sendRequest, disasmAddr, selectedFrame]);

  /* ── Send breakpoints to debugger ── */
  const sendBreakpoints = useCallback(async () => {
    const byFile = new Map<string, Breakpoint[]>();
    for (const bp of breakpoints) {
      if (!byFile.has(bp.file)) byFile.set(bp.file, []);
      byFile.get(bp.file)!.push(bp);
    }
    for (const [file, bps] of byFile) {
      await sendRequest("setBreakpoints", {
        source: { path: file },
        breakpoints: bps
          .filter(b => b.enabled)
          .map(b => ({
            line: b.line,
            condition: b.condition,
            hitCondition: b.hitCondition,
            logMessage: b.logMessage,
          })),
      });
    }
  }, [sendRequest, breakpoints]);

  /* ── Active frames (derived) ── */
  const frames = useMemo(() =>
    stackMap.get(activeThreadId) ?? [], [stackMap, activeThreadId]);

  /* ── DAP event handler ── */
  const handleMessage = useCallback(async (msg: any) => {
    if (msg.type === "response") {
      const cb = pendingRef.current.get(msg.request_seq);
      if (cb) { pendingRef.current.delete(msg.request_seq); cb(msg); }
      return;
    }
    if (msg.type !== "event") return;

    switch (msg.event) {

      case "initialized": {
        if (configDoneRef.current) break;
        configDoneRef.current = true;
        await sendBreakpoints();
        await sendRequest("configurationDone");
        setState("running");
        setOutput(p => [...p, `[debug] ✓ ${t("debug.sessionReady")}\n`]);
        break;
      }

      case "capabilities": {
        setCaps(msg.body?.capabilities ?? {});
        break;
      }

      case "stopped": {
        setState("paused");
        const threadId: number = msg.body?.threadId ?? 1;
        activeThreadIdRef.current = threadId;
        setActiveThreadId(threadId);

        // Fetch all threads
        const tResp = await sendRequest("threads");
        const allThreads: DapThread[] = tResp?.body?.threads ?? [{ id: threadId, name: "main" }];
        setThreads(allThreads);

        // Fetch stack for stopped thread
        const newFrames = await fetchStack(threadId);
        setStackMap(prev => new Map(prev).set(threadId, newFrames));
        setSelectedFrame(0);

        const frameId = newFrames[0]?.id ?? 0;
        selectedFrameIdRef.current = frameId;

        if (newFrames[0]?.source?.path && newFrames[0].line) {
          onPauseAt?.(newFrames[0].source.path, newFrames[0].line);
        }

        // Load variables
        if (frameId) await fetchFrameVars(frameId);
        await refreshWatches();
        break;
      }

      case "thread": {
        const tResp = await sendRequest("threads");
        setThreads(tResp?.body?.threads ?? []);
        break;
      }

      case "continued": {
        setState("running");
        setScopeVars([]);
        setExpandedKeys(new Set());
        setChildrenMap(new Map());
        break;
      }

      case "exited": {
        const code = msg.body?.exitCode;
        const codeStr = code !== undefined ? ` (exit code: ${code})` : "";
        setOutput(p => [...p, `\n[debug] ${t("debug.processExited")}${codeStr}\n`]);
        break;
      }

      case "terminated": {
        sessionEndedRef.current = true;
        intentionalRef.current = true;
        wsRef.current?.close();
        wsRef.current = null;
        await new Promise(r => setTimeout(r, 0));
        intentionalRef.current = false;
        setState("idle");
        setStackMap(new Map());
        setScopeVars([]);
        setThreads([]);
        if (rootPath) StopDebug(rootPath).catch(() => {});
        setOutput(p => [...p, `[debug] ${t("debug.sessionEnded")}\n`]);
        break;
      }

      case "output": {
        if (msg.body?.output) {
          setOutput(p => {
            const next = [...p, msg.body.output as string];
            return next.length > 1000 ? next.slice(-1000) : next;
          });
        }
        break;
      }
    }
  }, [sendBreakpoints, sendRequest, fetchStack, fetchFrameVars, refreshWatches, onPauseAt, rootPath]);

  useEffect(() => { handleMessageRef.current = handleMessage; }, [handleMessage]);
  useEffect(() => {
    outputEndRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [output]);

  useEffect(() => {
    if (!rootPath) { setMainPkgs([]); setSelectedPkg("."); setIsWailsProject(false); return; }
    ScanMainPackages(rootPath)
      .then(pkgs => {
        setIsWailsProject(false);
        const l = pkgs ?? [];
        setMainPkgs(l);
        setSelectedPkg(l[0] ?? ".");
      })
      .catch((err: any) => {
        const msg = String(err);
        if (msg.includes("wails-project")) {
          setIsWailsProject(true);
          setMainPkgs([]);
        }
      });
  }, [rootPath]);

  /* ── WebSocket connect ── */
  const connect = useCallback(async (proxyPort: number, adapterPort: number) => {
    if (wsRef.current) { wsRef.current.close(); wsRef.current = null; }
    const ws = new WebSocket(`ws://127.0.0.1:${proxyPort}/dap?port=${adapterPort}`);
    wsRef.current = ws;
    const adapter = adapterRef.current;

    ws.onopen = async () => {
      configDoneRef.current = false;
      const adapterID = adapter === "lldb-dap" ? "lldb-dap" : "dlv";
      const initResp = await sendRequest("initialize", {
        clientName: "TianCan IDE",
        adapterID,
        pathFormat: "path",
        supportsVariableType: true,
        supportsVariablePaging: true,
        supportsRunInTerminalRequest: false,
        supportsMemoryReferences: true,
        supportsReadMemoryRequest: true,
        supportsDisassembleRequest: true,
        supportsStepBack: adapter === "dlv",
        supportsConditionalBreakpoints: true,
        supportsHitConditionalBreakpoints: true,
        supportsLogPoints: true,
        supportsEvaluateForHovers: true,
        supportsSetVariable: true,
      });
      if (initResp?.body) setCaps(initResp.body);

      let launchArgs: Record<string, unknown>;
      if (adapter === "lldb-dap") {
        // lldb-dap launch arguments (C/C++/Rust/Swift)
        launchArgs = {
          program: rootPath,
          stopOnEntry: false,
          cwd: rootPath,
        };
      } else if (adapter === "debugpy") {
        // debugpy launch arguments (Python)
        launchArgs = {
          program: rootPath,
          stopOnEntry: false,
          cwd: rootPath,
          console: "integratedTerminal",
        };
      } else if (adapter === "js-debug") {
        // js-debug launch arguments (Node.js/JavaScript)
        launchArgs = {
          program: rootPath,
          stopOnEntry: false,
          cwd: rootPath,
          console: "integratedTerminal",
        };
      } else if (adapter === "java-debug") {
        // java-debug launch arguments (Java)
        launchArgs = {
          mainClass: rootPath,
          stopOnEntry: false,
          cwd: rootPath,
        };
      } else if (adapter === "dart") {
        // Dart/Flutter launch arguments
        launchArgs = {
          program: rootPath,
          stopOnEntry: false,
          cwd: rootPath,
          console: "integratedTerminal",
        };
      } else {
        // dlv launch arguments (Go)
        launchArgs = {
          mode: "debug",
          program: rootPath
            ? (selectedPkg === "." ? rootPath : `${rootPath}/${selectedPkg}`)
            : rootPath,
          stopOnEntry: false,
          redirectStdout: true,
          redirectStderr: true,
        };
        if (buildFlagsRef.current) launchArgs.buildFlags = buildFlagsRef.current;
      }
      const launchResp = await sendRequest("launch", launchArgs);
      if (launchResp && launchResp.success === false) {
        const msg = launchResp.message ?? "launch failed";
        setState("error");
        setError(`${adapterID} ${t("debug.launchFail")}: ${msg}`);
      }
    };
    ws.onmessage = (ev) => {
      parseMessages(typeof ev.data === "string" ? ev.data : "")
        .forEach(msg => handleMessageRef.current(msg));
    };
    ws.onerror = () => {
      if (intentionalRef.current || sessionEndedRef.current) return;
      setState("error"); setError(t("debug.wsConnectFail"));
    };
    ws.onclose = () => {
      if (wsRef.current === ws) wsRef.current = null;
      if (!intentionalRef.current && !sessionEndedRef.current) {
        setState(prev =>
          (prev === "running" || prev === "paused") ? "idle" : prev);
      }
    };
  }, [sendRequest, parseMessages, rootPath, selectedPkg]);

  /* ── Run-fallback (no DAP adapter) ── */
  const runFallbackTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const adapterRef = useRef<string>(""); // detected adapter for current session

  const startRunFallback = useCallback(async () => {
    if (!rootPath) return;
    setState("starting"); setError(null); setOutput([]);
    try {
      await StopProject(rootPath).catch(() => {});
      // Detect run command via scan
      const configs = await ScanRunConfigs(rootPath).catch(() => [] as { label: string; cmd: string }[]);
      const cmd = configs?.[0]?.cmd;
      if (!cmd) { setState("error"); setError(t("debug.noRunCmd")); return; }
      setOutput([`${t("debug.starting")}: ${cmd}\n`]);
      await StartProjectWithCmd(rootPath, cmd);
      setState("running");
      setOutput(p => [...p, `✓ ${t("debug.runModeStarted")}\n`]);
      // Poll process output
      runFallbackTimerRef.current = setInterval(async () => {
        const lines = await GetProcessOutput(rootPath).catch(() => [] as string[]);
        if (lines && lines.length > 0) {
          setOutput(prev => {
            const next = [...prev, ...lines.map(l => l + "\n")];
            return next.length > 1000 ? next.slice(-1000) : next;
          });
        }
      }, 500);
    } catch (err) {
      setState("error"); setError(String(err));
    }
  }, [rootPath]);

  const stopRunFallback = useCallback(async () => {
    if (runFallbackTimerRef.current) {
      clearInterval(runFallbackTimerRef.current);
      runFallbackTimerRef.current = null;
    }
    if (rootPath) await StopProject(rootPath).catch(() => {});
    setState("idle");
  }, [rootPath]);

  /* ── Start / Stop / Restart ── */
  const handleStart = useCallback(async () => {
    if (!rootPath) return;

    // Detect which DAP adapter to use
    const adapter = await DetectAdapterType(rootPath).catch(() => "") as string;
    adapterRef.current = adapter;

    // No DAP adapter → run-fallback
    if (!adapter) {
      await startRunFallback();
      return;
    }

    // DAP debug mode
    setState("starting"); setError(null); setOutput([]);
    setStackMap(new Map()); setScopeVars([]); setThreads([]);
    setExpandedKeys(new Set()); setChildrenMap(new Map());
    bufRef.current = ""; _seq = 1; pendingRef.current.clear();
    sessionEndedRef.current = false;
    try {
      await StopProject(rootPath).catch(() => {});
      // Adapter-specific install check and status message
      let readyMsg = `${t("debug.startingDebugger")}...\n`;
      let downloadMsg = `${t("debug.firstTimeInstall")}...\n`;
      switch (adapter) {
        case "dlv": {
          const envCfg = await DetectRunEnv(rootPath).catch(() => null);
          buildFlagsRef.current = (envCfg as any)?.buildFlags ?? "";
          const ready = await IsDlvInstalled().catch(() => false);
          readyMsg = `${t("debug.startingDebugger")} (dlv)...\n`;
          downloadMsg = `${t("debug.firstTimeDownload")} dlv (~20MB)...\n`;
          setOutput([ready ? readyMsg : downloadMsg]);
          break;
        }
        case "lldb-dap": {
          const ready = await IsLldbDapInstalled().catch(() => false);
          readyMsg = `${t("debug.startingDebugger")} (lldb-dap)...\n`;
          downloadMsg = `${t("debug.firstTimeDownload")} lldb-dap...\n`;
          setOutput([ready ? readyMsg : downloadMsg]);
          break;
        }
        case "debugpy": {
          const ready = await IsDebugpyInstalled().catch(() => false);
          readyMsg = `${t("debug.startingDebugger")} (debugpy)...\n`;
          downloadMsg = `${t("debug.firstTimeInstall")} debugpy (pip install debugpy)...\n`;
          setOutput([ready ? readyMsg : downloadMsg]);
          break;
        }
        case "js-debug": {
          const ready = await IsJsDebugInstalled().catch(() => false);
          readyMsg = `${t("debug.startingDebugger")} (js-debug)...\n`;
          downloadMsg = `${t("debug.firstTimeDownload")} js-debug-dap...\n`;
          setOutput([ready ? readyMsg : downloadMsg]);
          break;
        }
        case "java-debug": {
          const ready = await IsJavaDebugInstalled().catch(() => false);
          readyMsg = `${t("debug.startingDebugger")} (java-debug)...\n`;
          downloadMsg = `${t("debug.firstTimeDownload")} java-debug...\n`;
          setOutput([ready ? readyMsg : downloadMsg]);
          break;
        }
        case "dart": {
          const ready = await IsDartInstalled().catch(() => false);
          readyMsg = `${t("debug.startingDebugger")} (Dart/Flutter)...\n`;
          downloadMsg = `${t("debug.dartNotInstalled")}...\n`;
          setOutput([ready ? readyMsg : downloadMsg]);
          break;
        }
        default:
          setOutput([readyMsg]);
      }
      let proxyPort = proxyPortRef.current;
      if (!proxyPort) { proxyPort = await StartAndGetPort(); proxyPortRef.current = proxyPort; }
      const adapterPort = await StartDebug(rootPath);
      if (!adapterPort) {
        // No adapter detected — fall back to run mode
        await startRunFallback();
        return;
      }
      setOutput(p => [...p, `${t("debug.debuggerReadyConnecting")}\n`]);
      await connect(proxyPort, adapterPort);
    } catch (err) {
      setState("error"); setError(String(err));
    }
  }, [rootPath, connect, startRunFallback]);

  const handleStop = useCallback(async () => {
    // Run-fallback mode
    if (!adapterRef.current) {
      await stopRunFallback();
      return;
    }
    // DAP debug mode
    sessionEndedRef.current = true; intentionalRef.current = true;
    pendingRef.current.clear();
    sendRequest("disconnect", { terminateDebuggee: true }).catch(() => {});
    wsRef.current?.close(); wsRef.current = null;
    if (rootPath) await StopDebug(rootPath).catch(() => {});
    intentionalRef.current = false;
    setState("idle"); setStackMap(new Map()); setScopeVars([]); setThreads([]);
  }, [rootPath, sendRequest, stopRunFallback]);

  const handleRestart = useCallback(async () => {
    // Always do a full stop+start rather than the DAP restart command.
    // The DAP restart causes dlv to re-send the initialized event, but
    // configDoneRef.current is still true so we never send configurationDone
    // and dlv keeps the new process paused indefinitely.
    await handleStop(); await handleStart();
  }, [handleStop, handleStart]);

  /* ── Step controls ── */
  const tid = () => activeThreadIdRef.current;
  const handleContinue  = useCallback(() => { sendRequest("continue",  { threadId: tid() }); setState("running"); }, [sendRequest]);
  const handleNext      = useCallback(() => sendRequest("next",     { threadId: tid() }), [sendRequest]);
  const handleStepIn    = useCallback(() => sendRequest("stepIn",   { threadId: tid() }), [sendRequest]);
  const handleStepOut   = useCallback(() => sendRequest("stepOut",  { threadId: tid() }), [sendRequest]);
  const handlePause     = useCallback(() => sendRequest("pause",    { threadId: tid() }), [sendRequest]);

  /* ── Frame selection ── */
  const handleFrameClick = useCallback(async (frame: StackFrame, idx: number) => {
    setSelectedFrame(idx);
    selectedFrameIdRef.current = frame.id;
    if (frame.source?.path && frame.line) onNavigate?.(frame.source.path, frame.line);
    await fetchFrameVars(frame.id);
    await refreshWatches();
  }, [fetchFrameVars, refreshWatches, onNavigate]);

  /* ── Thread selection ── */
  const handleThreadClick = useCallback(async (threadId: number) => {
    activeThreadIdRef.current = threadId;
    setActiveThreadId(threadId);
    if (!stackMap.has(threadId)) {
      const frames = await fetchStack(threadId);
      setStackMap(prev => new Map(prev).set(threadId, frames));
    }
    setSelectedFrame(0);
    const threadFrames = stackMap.get(threadId) ?? [];
    const frameId = threadFrames[0]?.id;
    if (frameId) { selectedFrameIdRef.current = frameId; await fetchFrameVars(frameId); }
  }, [stackMap, fetchStack, fetchFrameVars]);

  /* ── Set variable value ── */
  const handleSetVariable = useCallback(async (v: DapVariable, newValue: string) => {
    if (!caps.supportsSetVariable || !v.evaluateName) return;
    await sendRequest("setVariable", {
      variablesReference: 0, // will be overridden by actual scope ref
      name: v.name,
      value: newValue,
    });
    // Refresh variables
    await fetchFrameVars(selectedFrameIdRef.current);
  }, [caps, sendRequest, fetchFrameVars]);

  /* ── Watch ── */
  const addWatch = useCallback(async () => {
    if (!newWatch.trim()) return;
    const item: WatchItem = { expr: newWatch.trim() };
    if (state === "paused") {
      const r = await sendRequest("evaluate", {
        expression: item.expr, frameId: selectedFrameIdRef.current, context: "watch",
      }).catch(() => null);
      item.result = r?.body?.result ?? "<error>";
      item.error = !r?.body?.result;
    }
    setWatches(prev => [...prev, item]);
    setNewWatch("");
  }, [newWatch, state, sendRequest]);

  const removeWatch = (idx: number) =>
    setWatches(prev => prev.filter((_, i) => i !== idx));

  /* ── Expose handle ── */
  useImperativeHandle(ref, () => ({ startDebug: handleStart, evaluate }), [handleStart, evaluate]);
  useEffect(() => () => { wsRef.current?.close(); }, []);
  useEffect(() => { onDebugStateChange?.(state); }, [state]);

  /* ─────────────── RENDER ─────────────── */
  const canControl = state === "paused";
  const isActive   = state === "running" || state === "paused" || state === "starting";

  /* ── Keyboard shortcuts (F5/F10/F11) ── */
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "F5") {
        e.preventDefault();
        if (!isActive) { handleStart(); }
        else if (state === "paused") { handleContinue(); }
      } else if (e.key === "F10" && state === "paused") {
        e.preventDefault(); handleNext();
      } else if (e.key === "F11" && state === "paused") {
        e.preventDefault();
        if (e.shiftKey) handleStepOut(); else handleStepIn();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [state, isActive, handleStart, handleContinue, handleNext, handleStepIn, handleStepOut]);

  return (
    <div className="debug-panel">

      {/* ── Toolbar ── */}
      <div className="debug-toolbar">
        <span className="debug-toolbar-title"><Bug size={12} style={{ opacity: 0.7 }} /> {t("debug.title")}</span>
        <div className="debug-toolbar-controls">
          {!isActive ? (
            <button className="debug-btn debug-btn-start" onClick={handleStart}
              disabled={!rootPath || isWailsProject} title={isWailsProject ? t("debug.wailsNoDebug") : t("debug.startDebugF5")}>
              <Play size={12} fill="currentColor" />
            </button>
          ) : (
            <>
              <button className="debug-btn" onClick={handleContinue} disabled={!canControl} title={t("debug.continueF5")}>
                <Play size={11} fill="currentColor" />
              </button>
              <button className="debug-btn" onClick={handlePause} disabled={!canControl} title={t("debug.pause")}>
                <Square size={11} />
              </button>
              <button className="debug-btn" onClick={handleNext} disabled={!canControl} title={t("debug.stepOverF10")}>
                <SkipForward size={11} />
              </button>
              <button className="debug-btn" onClick={handleStepIn} disabled={!canControl} title={t("debug.stepInF11")}>
                <ArrowDownLeft size={11} />
              </button>
              <button className="debug-btn" onClick={handleStepOut} disabled={!canControl} title={t("debug.stepOutShiftF11")}>
                <ArrowUpRight size={11} />
              </button>
              {caps.supportsStepBack && (
                <button className="debug-btn" onClick={handleStepBack} disabled={!canControl} title={t("debug.stepBack")}>
                  <Rewind size={11} />
                </button>
              )}
              <button className="debug-btn" onClick={handleRestart} title={t("debug.restart")}>
                <RotateCcw size={11} />
              </button>
              <button className="debug-btn debug-btn-stop" onClick={handleStop} title={t("debug.stop")}>
                <Square size={11} fill="currentColor" />
              </button>
            </>
          )}
        </div>
        <span className={`debug-state-badge debug-state-${state}`}>
          {state === "starting" && <Loader2 size={9} className="spin" />}
          {{ idle: t("debug.stateIdle"), starting: t("debug.stateStarting"), running: t("debug.stateRunning"), paused: t("debug.statePaused"), error: t("debug.stateError") }[state]}
        </span>
      </div>

      {/* ── Extra toolbar: memory / disasm ── */}
      {isActive && (caps.supportsReadMemoryRequest || caps.supportsDisassembleRequest) && (
        <div className="debug-extra-toolbar">
          {caps.supportsReadMemoryRequest && (
            <button className="debug-extra-btn" onClick={() => toggleSection("memory")} title={t("debug.memoryInspect")}>
              <MemoryStick size={12} /> {t("debug.memory")}
            </button>
          )}
          {caps.supportsDisassembleRequest && (
            <button className="debug-extra-btn" onClick={() => toggleSection("disasm")} title={t("debug.disasm")}>
              <Cpu size={12} /> {t("debug.disasm")}
            </button>
          )}
        </div>
      )}

      {/* ── Program selector ── */}
      {mainPkgs.length > 1 && (
        <div className="debug-program-row">
          <span className="debug-program-label">{t("debug.target")}</span>
          <select className="debug-program-select" value={selectedPkg}
            onChange={e => setSelectedPkg(e.target.value)} disabled={isActive}>
            {mainPkgs.map(p => <option key={p} value={p}>{p}</option>)}
          </select>
        </div>
      )}

      {error && (
        <div className="debug-error">
          <AlertTriangle size={12} /><span>{error}</span>
        </div>
      )}
      {isWailsProject && (
        <div className="debug-wails-hint">
          <AlertTriangle size={12} />
          <span>{t("debug.wailsHint")}</span>
        </div>
      )}
      {state === "idle" && !error && !isWailsProject && (
        <div className="debug-hint">
          {adapterRef.current === "dlv"
            ? (mainPkgs.length === 0 ? t("debug.noGoMain") : t("debug.clickToDebug", { pkg: selectedPkg === "." ? t("debug.rootDir") : selectedPkg }))
            : adapterRef.current === "lldb-dap"
              ? t("debug.clickToDebugLLDB")
              : t("debug.clickToRun")}
        </div>
      )}

      {/* ── Threads ── */}
      {threads.length > 1 && (
        <div className="debug-threads">
          {threads.map(t => (
            <button key={t.id}
              className={`debug-thread-btn${t.id === activeThreadId ? " active" : ""}`}
              onClick={() => handleThreadClick(t.id)}>
              T{t.id} {t.name}
            </button>
          ))}
        </div>
      )}

      {/* ── Call Stack ── */}
      {frames.length > 0 && (
        <div className="debug-section">
          <div className="debug-section-header" onClick={() => toggleSection("stack")}>
            {isOpen("stack") ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
            {t("debug.callStack")} ({frames.length})
          </div>
          {isOpen("stack") && (
            <VirtualList
              items={frames}
              height={Math.min(frames.length * ITEM_HEIGHT, 200)}
              keyFn={(f) => String(f.id)}
              renderItem={(f, i) => (
                <div
                  className={`debug-frame-item${selectedFrame === i ? " active" : ""}`}
                  onClick={() => handleFrameClick(f, i)}
                  title={f.source?.path ? `${f.source.path}:${f.line}` : f.name}
                >
                  <span className="debug-frame-index">{i}</span>
                  <span className="debug-frame-name">{f.name}</span>
                  <span className="debug-frame-loc">
                    {f.source?.name ?? f.source?.path?.split("/").pop() ?? ""}
                    {f.line ? `:${f.line}` : ""}
                  </span>
                </div>
              )}
            />
          )}
        </div>
      )}

      {/* ── Variables (virtual, recursive) ── */}
      {flatVars.length > 0 && (
        <div className="debug-section">
          <div className="debug-section-header" onClick={() => toggleSection("vars")}>
            {isOpen("vars") ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
            {t("debug.variables")} ({scopeVars.filter(v => v.type !== "__scope__").length})
          </div>
          {isOpen("vars") && (
            <VirtualList
              items={flatVars}
              height={Math.min(flatVars.length * ITEM_HEIGHT, 300)}
              keyFn={(v) => v._key ?? v.name}
              renderItem={(v) => {
                if (v.type === "__scope__") {
                  return (
                    <div className="debug-scope-header" style={{ paddingLeft: 8 }}>
                      {v.name}
                    </div>
                  );
                }
                const indent = ((v._depth ?? 0) + 1) * 14;
                const expandable = v.variablesReference > 0;
                const expanded = v._key ? expandedKeys.has(v._key) : false;
                return (
                  <div className="debug-var-row" style={{ paddingLeft: indent }}>
                    <span
                      className="debug-var-expand"
                      onClick={() => handleToggleVar(v)}
                    >
                      {expandable
                        ? (expanded ? <ChevronDown size={10} /> : <ChevronRight size={10} />)
                        : <span style={{ width: 10, display: "inline-block" }} />
                      }
                    </span>
                    <span className="debug-var-name">{v.name}</span>
                    {v.type && <span className="debug-var-type">{v.type}</span>}
                    <span className="debug-var-value" title={v.value}>{v.value}</span>
                    {v.memoryReference && (
                      <button
                        className="debug-var-mem-btn"
                        title={t("debug.viewMemory")}
                        onClick={() => {
                          setMemAddress(v.memoryReference!);
                          toggleSection("memory");
                          if (!openSections.has("memory"))
                            setOpenSections(p => new Set(p).add("memory"));
                        }}
                      >
                        <MemoryStick size={9} />
                      </button>
                    )}
                  </div>
                );
              }}
            />
          )}
        </div>
      )}

      {/* ── Watch expressions ── */}
      <div className="debug-section">
        <div className="debug-section-header" onClick={() => toggleSection("watch")}>
          {isOpen("watch") ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
          <Eye size={10} style={{ marginRight: 4 }} />{t("debug.watch")} ({watches.length})
        </div>
        {isOpen("watch") && (
          <>
            <div className="debug-watch-add">
              <input
                className="debug-watch-input"
                placeholder={t("debug.addWatchPlaceholder")}
                value={newWatch}
                onChange={e => setNewWatch(e.target.value)}
                onKeyDown={e => e.key === "Enter" && addWatch()}
              />
              <button className="debug-watch-add-btn" onClick={addWatch} title={t("debug.add")}>
                <Plus size={11} />
              </button>
            </div>
            {watches.map((w, i) => (
              <div key={i} className={`debug-watch-item${w.error ? " error" : ""}`}>
                <span className="debug-watch-expr">{w.expr}</span>
                <span className="debug-watch-result">{w.result ?? "—"}</span>
                <button className="debug-watch-del" onClick={() => removeWatch(i)}>
                  <X size={9} />
                </button>
              </div>
            ))}
          </>
        )}
      </div>

      {/* ── Breakpoints ── */}
      <div className="debug-section">
        <div className="debug-section-header" onClick={() => toggleSection("breakpoints")}>
          {isOpen("breakpoints") ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
          {t("debug.breakpoints")} ({breakpoints.length})
          {breakpoints.length > 0 && (
            <button className="debug-clear-bp-btn"
              onClick={e => { e.stopPropagation(); onClearBreakpoints?.(); }}
              title={t("debug.clearAllBp")}>
              <Trash2 size={10} /> {t("debug.clearAll")}
            </button>
          )}
        </div>
        {isOpen("breakpoints") && breakpoints.length === 0 && (
          <div className="debug-bp-empty">{t("debug.noBreakpoints")}</div>
        )}
        {isOpen("breakpoints") && breakpoints.map((bp, i) => (
          <div key={i} className={`debug-bp-item${!bp.enabled ? " disabled" : ""}`}>
            <button
              className="debug-bp-toggle"
              onClick={e => { e.stopPropagation(); onToggleBreakpoint?.(bp.file, bp.line, !bp.enabled); }}
              title={bp.enabled ? t("debug.disableBp") : t("debug.enableBp")}
            >
              {bp.enabled
                ? <Circle size={9} className="debug-bp-dot" fill="currentColor" />
                : <Ban size={9} className="debug-bp-dot-disabled" />}
            </button>
            <div className="debug-bp-info" onClick={() => onNavigate?.(bp.file, bp.line)}>
              <span className="debug-bp-file">{bp.file.split("/").pop()}</span>
              <span className="debug-bp-line">:{bp.line}</span>
              {bp.condition && <span className="debug-bp-cond" title={bp.condition}>⚙</span>}
              {bp.logMessage && <span className="debug-bp-log" title={bp.logMessage}>📝</span>}
            </div>
            <button className="debug-bp-edit"
              onClick={e => { e.stopPropagation(); setEditingBp(bp); }} title={t("debug.editBreakpoint")}>
              <Pencil size={9} />
            </button>
            <button className="debug-bp-del"
              onClick={e => { e.stopPropagation(); onDeleteBreakpoint?.(bp.file, bp.line); }}
              title={t("debug.delete")}>
              <X size={9} />
            </button>
          </div>
        ))}
      </div>

      {/* ── Memory Inspector ── */}
      {isOpen("memory") && (
        <div className="debug-section">
          <div className="debug-section-header" onClick={() => toggleSection("memory")}>
            <ChevronDown size={10} /><MemoryStick size={10} style={{ marginRight: 4 }} />{t("debug.memoryInspect")}
          </div>
          <div className="debug-memory-bar">
            <input className="debug-memory-addr" placeholder={t("debug.addrPlaceholder")}
              value={memAddress} onChange={e => setMemAddress(e.target.value)} />
            <input className="debug-memory-count" placeholder={t("debug.byteCount")} type="number"
              value={memCount} onChange={e => setMemCount(e.target.value)} style={{ width: 60 }} />
            <button className="debug-memory-btn" onClick={handleReadMemory}>{t("debug.read")}</button>
          </div>
          {memResult && (
            <div className="debug-memory-view">
              <div className="debug-memory-addr-label">{memResult.address}</div>
              {memResult.hex.map((row, i) => (
                <div key={i} className="debug-memory-row">
                  <span className="debug-memory-offset">
                    {(i * 16).toString(16).padStart(4, "0")}
                  </span>
                  <span className="debug-memory-hex">{row}</span>
                  <span className="debug-memory-ascii">{memResult.ascii[i]}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {/* ── Disassembly ── */}
      {isOpen("disasm") && (
        <div className="debug-section">
          <div className="debug-section-header" onClick={() => toggleSection("disasm")}>
            <ChevronDown size={10} /><Cpu size={10} style={{ marginRight: 4 }} />{t("debug.disasm")}
          </div>
          <div className="debug-memory-bar">
            <input className="debug-memory-addr" placeholder={t("debug.addrEmptyIP")}
              value={disasmAddr} onChange={e => setDisasmAddr(e.target.value)} />
            <button className="debug-memory-btn" onClick={handleDisassemble}>{t("debug.disassemble")}</button>
          </div>
          <div className="debug-disasm-view">
            {disasmResult.map((ins, i) => (
              <div key={i} className={`debug-disasm-row${ins.address === frames[selectedFrame]?.instructionPointerReference ? " current" : ""}`}>
                <span className="debug-disasm-addr">{ins.address}</span>
                {ins.symbol && <span className="debug-disasm-sym">{ins.symbol}</span>}
                <span className="debug-disasm-ins">{ins.instruction}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* ── Output ── */}
      {output.length > 0 && (
        <div className="debug-section debug-output-section">
          <div className="debug-section-header" onClick={() => toggleSection("output")}>
            {isOpen("output") ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
            {t("debug.programOutput")}
          </div>
          {isOpen("output") && (
            <div className="debug-output-log">
              {output.map((line, i) => (
                <AnsiLine key={i} line={line} idx={i} />
              ))}
              <div ref={outputEndRef} />
            </div>
          )}
        </div>
      )}

      {/* ── Breakpoint editor modal ── */}
      {editingBp && (
        <BreakpointEditor
          bp={editingBp}
          onSave={patch => onUpdateBreakpoint?.(editingBp.file, editingBp.line, patch)}
          onClose={() => setEditingBp(null)}
        />
      )}
    </div>
  );
});

export default DebugPanel;
