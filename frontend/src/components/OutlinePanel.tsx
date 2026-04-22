/**
 * OutlinePanel — 显示当前文件的文档符号大纲（Document Symbol）
 * 由 LSP 提供数据，点击跳转到对应行
 */
import React, { useEffect, useRef, useState, useCallback } from "react";
import { useTranslation } from "../i18n";
import { DocumentSymbol, SYMBOL_KIND_ICON, getActiveLspClient } from "../lsp/client";

interface Props {
  activeFile: string | null;
  rootPath: string | null;
  langId: string | null;
  onGoToLine: (line: number, col?: number) => void;
  /** 面板是否可见，切换到大纲 tab 时触发刷新 */
  visible?: boolean;
}

function pathToUri(path: string): string {
  const p = path.startsWith("/") ? path : `/${path}`;
  return `file://${p}`;
}

function SymbolTree({
  symbols,
  depth = 0,
  onSelect,
}: {
  symbols: DocumentSymbol[];
  depth?: number;
  onSelect: (s: DocumentSymbol) => void;
}) {
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});

  return (
    <>
      {symbols.map((sym, i) => {
        const key = `${sym.name}-${sym.range.startLine}-${i}`;
        const hasChildren = sym.children && sym.children.length > 0;
        const isCollapsed = collapsed[key];
        const icon = SYMBOL_KIND_ICON[sym.kind] ?? "🔹";

        return (
          <React.Fragment key={key}>
            <div
              className="outline-item"
              style={{ paddingLeft: `${8 + depth * 14}px` }}
              onClick={() => onSelect(sym)}
            >
              {hasChildren ? (
                <span
                  className="outline-arrow"
                  onClick={(e) => {
                    e.stopPropagation();
                    setCollapsed((prev) => ({ ...prev, [key]: !prev[key] }));
                  }}
                >
                  {isCollapsed ? "▶" : "▼"}
                </span>
              ) : (
                <span className="outline-arrow-placeholder" />
              )}
              <span className="outline-icon">{icon}</span>
              <span className="outline-name">{sym.name}</span>
              {sym.detail && (
                <span className="outline-detail">{sym.detail}</span>
              )}
            </div>
            {hasChildren && !isCollapsed && (
              <SymbolTree
                symbols={sym.children!}
                depth={depth + 1}
                onSelect={onSelect}
              />
            )}
          </React.Fragment>
        );
      })}
    </>
  );
}

export default function OutlinePanel({ activeFile, rootPath, langId, onGoToLine, visible }: Props) {
  const { t } = useTranslation();
  const [symbols, setSymbols] = useState<DocumentSymbol[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const prevFileRef = useRef<string | null>(null);
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const fetchSymbols = useCallback(async (retryCount = 0) => {
    if (!activeFile || !rootPath || !langId) {
      setSymbols([]);
      return;
    }
    const client = getActiveLspClient(langId, rootPath);
    if (!client) {
      // LSP client 尚未创建，最多重试 8 次（约 4 秒）
      if (retryCount < 8) {
        retryTimerRef.current = setTimeout(() => fetchSymbols(retryCount + 1), 500);
      } else {
        setSymbols([]);
        setError(t("outline.lspNotConnected"));
      }
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const uri = pathToUri(activeFile);
      const cached = client.getCachedSymbols(uri);
      if (cached.length > 0) setSymbols(cached);
      const fresh = await client.fetchDocumentSymbols(uri);
      setSymbols(fresh);
    } catch (e: any) {
      setError(e?.message ?? t("outline.fetchFail"));
    } finally {
      setLoading(false);
    }
  }, [activeFile, rootPath, langId]);

  // 文件切换或面板变为可见时刷新
  useEffect(() => {
    if (retryTimerRef.current) clearTimeout(retryTimerRef.current);
    if (activeFile !== prevFileRef.current || visible) {
      prevFileRef.current = activeFile;
      fetchSymbols(0);
    }
  }, [activeFile, visible, fetchSymbols]);

  useEffect(() => () => { if (retryTimerRef.current) clearTimeout(retryTimerRef.current); }, []);

  // 订阅 LSP 实时更新
  useEffect(() => {
    if (!rootPath || !langId) return;
    const client = getActiveLspClient(langId, rootPath);
    if (!client) return;
    const prev = client.onSymbolsUpdated;
    client.onSymbolsUpdated = (uri, syms) => {
      if (activeFile && uri === pathToUri(activeFile)) {
        setSymbols(syms);
      }
      prev?.(uri, syms);
    };
    return () => {
      client.onSymbolsUpdated = prev;
    };
  }, [activeFile, rootPath, langId]);

  const handleSelect = useCallback((sym: DocumentSymbol) => {
    onGoToLine(sym.selectionRange.startLine + 1, sym.selectionRange.startChar + 1);
  }, [onGoToLine]);

  if (!activeFile) {
    return (
      <div className="outline-empty">
        <span>{t("outline.empty")}</span>
      </div>
    );
  }

  if (loading && symbols.length === 0) {
    return (
      <div className="outline-empty">
        <span>{t("outline.loading")}</span>
      </div>
    );
  }

  if (error) {
    return (
      <div className="outline-empty">
        <span style={{ color: "var(--color-error, #f44)" }}>{error}</span>
      </div>
    );
  }

  if (symbols.length === 0) {
    return (
      <div className="outline-empty">
        <span>{t("outline.noSymbols")}</span>
      </div>
    );
  }

  return (
    <div className="outline-panel">
      <div className="outline-header">
        <span>{t("outline.title")}</span>
        <button
          className="outline-refresh-btn"
          onClick={() => fetchSymbols(0)}
          title={t("outline.refreshTitle")}
        >
          ↺
        </button>
      </div>
      <div className="outline-tree">
        <SymbolTree symbols={symbols} onSelect={handleSelect} />
      </div>
    </div>
  );
}
