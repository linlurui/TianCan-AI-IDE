/**
 * CallHierarchyPanel — 调用层次面板
 * 显示当前光标处函数的调用者（Incoming）和被调用者（Outgoing）
 */
import React, { useState, useCallback, useEffect, useRef } from "react";
import { useTranslation } from "../i18n";
import { getActiveLspClient, getLspClient } from "../lsp/client";

interface CallItem {
  name: string;
  detail?: string;
  kind: number;
  uri: string;
  range: any;
  selectionRange: any;
  _raw: any;
}

interface CallEdge {
  from: CallItem;
  fromRanges: any[];
}

interface Props {
  activeFile: string | null;
  rootPath: string | null;
  langId: string | null;
  cursorLine: number;
  cursorCol: number;
  onOpenFile: (path: string, line: number) => void;
  lspPort?: number;
  monaco?: any;
}

function uriToPath(uri: string): string {
  return uri.replace(/^file:\/\//, "");
}

function kindIcon(kind: number): string {
  const map: Record<number, string> = {
    5: "🏛", 6: "⚡", 9: "🔨", 12: "⚙️",
  };
  return map[kind] ?? "🔹";
}

export default function CallHierarchyPanel({
  activeFile,
  rootPath,
  langId,
  cursorLine,
  cursorCol,
  onOpenFile,
  lspPort,
  monaco,
}: Props) {
  const { t } = useTranslation();
  const [root, setRoot] = useState<CallItem | null>(null);
  const [incoming, setIncoming] = useState<CallEdge[]>([]);
  const [outgoing, setOutgoing] = useState<CallEdge[]>([]);
  const [mode, setMode] = useState<"incoming" | "outgoing">("incoming");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const prepare = useCallback(async () => {
    if (!activeFile || !rootPath || !langId) return;
    let client = getActiveLspClient(langId, rootPath);
    if (!client && lspPort && monaco) {
      client = getLspClient(lspPort, langId, rootPath, monaco);
    }
    if (!client) { setError(t("callHierarchy.lspNotConnected")); return; }
    if (!client.isReady()) { setError(t("callHierarchy.lspInitializing")); return; }

    setLoading(true);
    setError(null);
    setRoot(null);
    setIncoming([]);
    setOutgoing([]);

    try {
      const uri = `file://${activeFile.startsWith("/") ? activeFile : "/" + activeFile}`;
      const items = await client.prepareCallHierarchy(uri, {
        lineNumber: cursorLine,
        column: cursorCol,
      } as any);

      if (!items || items.length === 0) {
        setError(t("callHierarchy.noInfo"));
        return;
      }

      const item = items[0];
      const rootItem: CallItem = {
        name: item.name,
        detail: item.detail,
        kind: item.kind,
        uri: item.uri,
        range: item.range,
        selectionRange: item.selectionRange,
        _raw: item,
      };
      setRoot(rootItem);

      const [inc, out] = await Promise.all([
        client.getIncomingCalls(item),
        client.getOutgoingCalls(item),
      ]);

      setIncoming(
        (inc ?? []).map((e: any) => ({
          from: {
            name: e.from.name,
            detail: e.from.detail,
            kind: e.from.kind,
            uri: e.from.uri,
            range: e.from.range,
            selectionRange: e.from.selectionRange,
            _raw: e.from,
          },
          fromRanges: e.fromRanges ?? [],
        }))
      );

      setOutgoing(
        (out ?? []).map((e: any) => ({
          from: {
            name: e.to.name,
            detail: e.to.detail,
            kind: e.to.kind,
            uri: e.to.uri,
            range: e.to.range,
            selectionRange: e.to.selectionRange,
            _raw: e.to,
          },
          fromRanges: e.fromRanges ?? [],
        }))
      );
    } catch (e: any) {
      setError(e?.message ?? t("callHierarchy.fetchFail"));
    } finally {
      setLoading(false);
    }
  }, [activeFile, rootPath, langId, cursorLine, cursorCol]);

  // Auto-trigger call hierarchy analysis on cursor change (debounced)
  useEffect(() => {
    if (!activeFile || !rootPath || !langId) return;
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      prepare();
    }, 400);
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [activeFile, rootPath, langId, cursorLine, cursorCol, prepare]);

  const handleItemClick = useCallback(
    (item: CallItem) => {
      const path = uriToPath(item.uri);
      const line = (item.selectionRange?.start?.line ?? item.range?.start?.line ?? 0) + 1;
      onOpenFile(path, line);
    },
    [onOpenFile]
  );

  const edges = mode === "incoming" ? incoming : outgoing;

  return (
    <div className="callhierarchy-panel">
      <div className="callhierarchy-toolbar">
        <button
          className="callhierarchy-prepare-btn"
          onClick={prepare}
          disabled={loading || !activeFile}
          title={t("callHierarchy.reanalyzeTitle")}
        >
          {loading ? t("callHierarchy.analyzing") : t("callHierarchy.reanalyze")}
        </button>
      </div>

      {error && (
        <div className="callhierarchy-error">{error}</div>
      )}

      {root && (
        <>
          <div className="callhierarchy-root">
            <span className="callhierarchy-icon">{kindIcon(root.kind)}</span>
            <span className="callhierarchy-name">{root.name}</span>
            {root.detail && (
              <span className="callhierarchy-detail">{root.detail}</span>
            )}
          </div>

          <div className="callhierarchy-mode-tabs">
            <button
              className={`callhierarchy-mode-tab${mode === "incoming" ? " active" : ""}`}
              onClick={() => setMode("incoming")}
            >
              {t("callHierarchy.callers")} ({incoming.length})
            </button>
            <button
              className={`callhierarchy-mode-tab${mode === "outgoing" ? " active" : ""}`}
              onClick={() => setMode("outgoing")}
            >
              {t("callHierarchy.callees")} ({outgoing.length})
            </button>
          </div>

          <div className="callhierarchy-list">
            {edges.length === 0 ? (
              <div className="callhierarchy-empty">
                {mode === "incoming" ? t("callHierarchy.noCallers") : t("callHierarchy.noCallees")}
              </div>
            ) : (
              edges.map((edge, i) => (
                <div
                  key={i}
                  className="callhierarchy-item"
                  onClick={() => handleItemClick(edge.from)}
                  title={uriToPath(edge.from.uri)}
                >
                  <span className="callhierarchy-icon">{kindIcon(edge.from.kind)}</span>
                  <div className="callhierarchy-item-info">
                    <span className="callhierarchy-name">{edge.from.name}</span>
                    {edge.from.detail && (
                      <span className="callhierarchy-detail">{edge.from.detail}</span>
                    )}
                    <span className="callhierarchy-file">
                      {uriToPath(edge.from.uri).split("/").pop()}
                      {edge.fromRanges[0] && `:${edge.fromRanges[0].start.line + 1}`}
                    </span>
                  </div>
                </div>
              ))
            )}
          </div>
        </>
      )}

      {!root && !loading && !error && (
        <div className="callhierarchy-hint">
          {t("callHierarchy.hint")}
        </div>
      )}
    </div>
  );
}
