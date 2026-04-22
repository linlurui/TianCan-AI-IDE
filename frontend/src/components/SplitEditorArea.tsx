/**
 * SplitEditorArea — 支持单窗格/水平分屏/垂直分屏的编辑器区域
 * 使用 editorStore 管理分屏状态
 */
import React, { useRef, useCallback, useState } from "react";
import { useTranslation } from "../i18n";
import { Columns2, Rows2, X as XIcon } from "lucide-react";
import Editor from "./Editor";
import type { Breakpoint } from "./Editor";
import { useEditorStore, SplitLayout } from "../store/editorStore";

interface Props {
  lspPort?: number;
  rootPath?: string;
  onMonacoMount?: (monaco: any) => void;
  onEditorMount?: (editor: any) => void;
  renderSpecial?: (path: string) => React.ReactNode | null;
  breakpoints?: Breakpoint[];
  onBreakpointToggle?: (file: string, line: number) => void;
  pausedAt?: { file: string; line: number } | null;
  onEvaluateExpr?: (expr: string) => Promise<string | null>;
  onCursorChange?: (line: number, col: number) => void;
  onSave: (path: string) => void;
  pluginRecBanner?: React.ReactNode;
}

export default function SplitEditorArea({
  lspPort, rootPath, onMonacoMount, onEditorMount, renderSpecial,
  breakpoints, onBreakpointToggle, pausedAt, onEvaluateExpr,
  onCursorChange, onSave, pluginRecBanner,
}: Props) {
  const {
    tabs, activeTab, splitLayout, panes, activePaneId,
    openTab, closeTab, setActiveTab, updateTabContent,
    setSplitLayout, setActivePaneId, splitCurrentTab, closeSplit,
  } = useEditorStore();

  const dividerRef = useRef<number>(50); // percent
  const [dividerPct, setDividerPct] = useState(50);

  const handleDividerDrag = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    const container = (e.currentTarget as HTMLElement).parentElement!;
    const rect = container.getBoundingClientRect();
    const isHoriz = splitLayout === "horizontal";

    const onMove = (ev: MouseEvent) => {
      const pct = isHoriz
        ? ((ev.clientX - rect.left) / rect.width) * 100
        : ((ev.clientY - rect.top) / rect.height) * 100;
      const clamped = Math.max(20, Math.min(80, pct));
      dividerRef.current = clamped;
      setDividerPct(clamped);
    };
    const onUp = () => {
      document.removeEventListener("mousemove", onMove);
      document.removeEventListener("mouseup", onUp);
    };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  }, [splitLayout]);

  const handleChange = useCallback((path: string, value: string) => {
    updateTabContent(path, value);
  }, [updateTabContent]);

  // Split toolbar buttons
  const SplitControls = () => {
    const { t } = useTranslation();
    return (
    <div className="split-controls">
      <button
        className={`split-btn${splitLayout === "horizontal" ? " active" : ""}`}
        title={t("split.horizontal")}
        onClick={() => {
          if (splitLayout === "single") splitCurrentTab("horizontal");
          else if (splitLayout !== "horizontal") {
            closeSplit();
            setTimeout(() => splitCurrentTab("horizontal"), 0);
          }
        }}
      >
        <Columns2 size={14} />
      </button>
      <button
        className={`split-btn${splitLayout === "vertical" ? " active" : ""}`}
        title={t("split.vertical")}
        onClick={() => {
          if (splitLayout === "single") splitCurrentTab("vertical");
          else if (splitLayout !== "vertical") {
            closeSplit();
            setTimeout(() => splitCurrentTab("vertical"), 0);
          }
        }}
      >
        <Rows2 size={14} />
      </button>
      {splitLayout !== "single" && (
        <button
          className="split-btn"
          title={t("split.close")}
          onClick={closeSplit}
        >
          <XIcon size={14} />
        </button>
      )}
    </div>
  );
  }

  if (splitLayout === "single") {
    return (
      <div className="split-editor-area single">
        {pluginRecBanner}
        <div className="split-toolbar">
          <SplitControls />
        </div>
        <Editor
          tabs={tabs}
          activeTab={activeTab}
          onTabSelect={(path) => setActiveTab(path)}
          onTabClose={(path) => closeTab(path)}
          onChange={handleChange}
          onSave={onSave}
          onCursorChange={onCursorChange}
          lspPort={lspPort}
          rootPath={rootPath}
          onMonacoMount={onMonacoMount}
          onEditorMount={onEditorMount}
          renderSpecial={renderSpecial}
          breakpoints={breakpoints}
          onBreakpointToggle={onBreakpointToggle}
          pausedAt={pausedAt}
          onEvaluateExpr={onEvaluateExpr}
        />
      </div>
    );
  }

  const isHoriz = splitLayout === "horizontal";
  const leftPane = panes.left;
  const rightPane = panes.right;

  return (
    <div className="split-editor-area split">
      {pluginRecBanner}
      <div className="split-toolbar">
        <SplitControls />
      </div>
      <div
        className={`split-panes ${isHoriz ? "horizontal" : "vertical"}`}
      >
        {/* Left / Top pane */}
        <div
          className={`split-pane${activePaneId === "left" ? " active-pane" : ""}`}
          style={isHoriz
            ? { width: `${dividerPct}%` }
            : { height: `${dividerPct}%` }
          }
          onClick={() => setActivePaneId("left")}
        >
          <Editor
            tabs={leftPane.tabs}
            activeTab={leftPane.activeTab}
            onTabSelect={(path) => { setActivePaneId("left"); setActiveTab(path, "left"); }}
            onTabClose={(path) => closeTab(path, "left")}
            onChange={handleChange}
            onSave={onSave}
            onCursorChange={activePaneId === "left" ? onCursorChange : undefined}
            lspPort={lspPort}
            rootPath={rootPath}
            onMonacoMount={onMonacoMount}
            onEditorMount={activePaneId === "left" ? onEditorMount : undefined}
            renderSpecial={renderSpecial}
            breakpoints={breakpoints}
            onBreakpointToggle={onBreakpointToggle}
            pausedAt={pausedAt}
            onEvaluateExpr={onEvaluateExpr}
          />
        </div>

        {/* Divider */}
        <div
          className={`split-divider ${isHoriz ? "vertical-divider" : "horizontal-divider"}`}
          onMouseDown={handleDividerDrag}
        />

        {/* Right / Bottom pane */}
        <div
          className={`split-pane${activePaneId === "right" ? " active-pane" : ""}`}
          style={isHoriz
            ? { width: `${100 - dividerPct}%` }
            : { height: `${100 - dividerPct}%` }
          }
          onClick={() => setActivePaneId("right")}
        >
          <Editor
            tabs={rightPane.tabs}
            activeTab={rightPane.activeTab}
            onTabSelect={(path) => { setActivePaneId("right"); setActiveTab(path, "right"); }}
            onTabClose={(path) => closeTab(path, "right")}
            onChange={handleChange}
            onSave={onSave}
            onCursorChange={activePaneId === "right" ? onCursorChange : undefined}
            lspPort={lspPort}
            rootPath={rootPath}
            renderSpecial={renderSpecial}
            breakpoints={breakpoints}
            onBreakpointToggle={onBreakpointToggle}
            pausedAt={pausedAt}
            onEvaluateExpr={onEvaluateExpr}
          />
        </div>
      </div>
    </div>
  );
}
