import React, { useState, useRef, useCallback, useEffect } from "react";
import { useTranslation } from "../i18n";
import { Search, X, FileCode, Loader2, ChevronRight, FolderOpen } from "lucide-react";
import { GrepFiles } from "../bindings/filesystem";
import type { GrepMatch } from "../bindings/filesystem";

interface Project { rootPath: string; name: string }

interface SearchPanelProps {
  rootPath: string | null;
  projects?: Project[];
  onOpenFile: (filePath: string, line: number) => void;
}

interface GroupedResult {
  filePath: string;
  fileName: string;
  relPath: string;
  matches: GrepMatch[];
}

function highlightMatch(text: string, query: string): React.ReactNode {
  if (!query) return text;
  const idx = text.toLowerCase().indexOf(query.toLowerCase());
  if (idx === -1) return text;
  return (
    <>
      {text.slice(0, idx)}
      <mark className="search-highlight">{text.slice(idx, idx + query.length)}</mark>
      {text.slice(idx + query.length)}
    </>
  );
}

export default function SearchPanel({ rootPath, projects = [], onOpenFile }: SearchPanelProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<GrepMatch[]>([]);
  const [loading, setLoading] = useState(false);
  const [searched, setSearched] = useState(false);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [selectedRoot, setSelectedRoot] = useState<string | null>(rootPath);
  const inputRef = useRef<HTMLInputElement>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Sync selectedRoot when active project changes, but only if user hasn't manually picked one
  useEffect(() => {
    setSelectedRoot(rootPath);
    setResults([]);
    setSearched(false);
    setQuery("");
  }, [rootPath]);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const doSearch = useCallback(async (q: string, root?: string | null) => {
    const searchRoot = root ?? selectedRoot;
    if (!searchRoot || !q.trim()) {
      setResults([]);
      setSearched(false);
      return;
    }
    setLoading(true);
    setSearched(true);
    try {
      const res = await GrepFiles(searchRoot, q.trim(), 200);
      setResults(res ?? []);
    } catch (e) {
      console.error("search:", e);
      setResults([]);
    } finally {
      setLoading(false);
    }
  }, [selectedRoot]);

  const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const q = e.target.value;
    setQuery(q);
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => doSearch(q), 300);
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      if (debounceRef.current) clearTimeout(debounceRef.current);
      doSearch(query);
    }
    if (e.key === "Escape") {
      setQuery("");
      setResults([]);
      setSearched(false);
    }
  };

  const clearSearch = () => {
    setQuery("");
    setResults([]);
    setSearched(false);
    inputRef.current?.focus();
  };

  const toggleCollapse = (filePath: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(filePath)) next.delete(filePath);
      else next.add(filePath);
      return next;
    });
  };

  // Group results by file
  const grouped: GroupedResult[] = [];
  const seen = new Map<string, GroupedResult>();
  for (const m of results) {
    if (!seen.has(m.filePath)) {
      const relPath = selectedRoot ? m.filePath.slice(selectedRoot.length + 1) : m.filePath;
      const g: GroupedResult = { filePath: m.filePath, fileName: m.fileName, relPath, matches: [] };
      seen.set(m.filePath, g);
      grouped.push(g);
    }
    seen.get(m.filePath)!.matches.push(m);
  }

  const totalMatches = results.length;
  const totalFiles = grouped.length;

  // Build project options: always include all known projects; ensure selectedRoot is an option
  const projectOptions: Project[] = projects.length > 0 ? projects : (selectedRoot ? [{ rootPath: selectedRoot, name: selectedRoot.split("/").pop() ?? selectedRoot }] : []);

  const handleProjectChange = (newRoot: string) => {
    setSelectedRoot(newRoot);
    setResults([]);
    setSearched(false);
    if (query.trim()) {
      if (debounceRef.current) clearTimeout(debounceRef.current);
      debounceRef.current = setTimeout(() => doSearch(query, newRoot), 100);
    }
  };

  return (
    <div className="search-panel">
      {/* ── Project selector ── */}
      {projectOptions.length > 1 && (
        <div className="search-project-selector">
          <FolderOpen size={11} className="search-project-icon" />
          <select
            className="search-project-select"
            value={selectedRoot ?? ""}
            onChange={(e) => handleProjectChange(e.target.value)}
          >
            {projectOptions.map((p) => (
              <option key={p.rootPath} value={p.rootPath}>{p.name}</option>
            ))}
          </select>
        </div>
      )}

      <div className="search-input-wrap">
        <Search size={13} className="search-input-icon" />
        <input
          ref={inputRef}
          className="search-input"
          placeholder={t("search.placeholder")}
          value={query}
          onChange={handleChange}
          onKeyDown={handleKeyDown}
          spellCheck={false}
        />
        {query && (
          <button className="search-clear-btn" onClick={clearSearch} title={t("search.clear")}>
            <X size={12} />
          </button>
        )}
      </div>

      {!selectedRoot && (
        <div className="search-hint">{t("search.openProjectFirst")}</div>
      )}

      {loading && (
        <div className="search-loading">
          <Loader2 size={16} className="spin" />
          <span>{t("search.searching")}</span>
        </div>
      )}

      {!loading && searched && query && (
        <div className="search-summary">
          {totalMatches > 0
            ? t("search.resultSummary", { matches: totalMatches, files: totalFiles })
            : t("search.noMatch")}
        </div>
      )}

      <div className="search-results">
        {grouped.map((g) => {
          const isCollapsed = collapsed.has(g.filePath);
          return (
            <div key={g.filePath} className="search-file-group">
              <div
                className="search-file-header"
                onClick={() => toggleCollapse(g.filePath)}
                title={g.filePath}
              >
                <ChevronRight
                  size={11}
                  className={`search-chevron${isCollapsed ? "" : " open"}`}
                />
                <FileCode size={13} className="search-file-icon" />
                <span className="search-file-name">{g.fileName}</span>
                <span className="search-file-path">{g.relPath}</span>
                <span className="search-file-count">{g.matches.length}</span>
              </div>
              {!isCollapsed && (
                <div className="search-file-matches">
                  {g.matches.map((m, i) => (
                    <div
                      key={i}
                      className="search-match-row"
                      onClick={() => onOpenFile(m.filePath, m.lineNumber)}
                      title={t("analysis.line", { n: m.lineNumber })}
                    >
                      <span className="search-line-num">{m.lineNumber}</span>
                      <span className="search-line-text">
                        {highlightMatch(m.lineText, query)}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
