import React, { useEffect, useState, useRef } from "react";
import { useTranslation } from "../i18n";
import { ScrollText, Trash2 } from "lucide-react";
import { GetProcessOutput } from "../bindings/process";
import { AnsiLine } from "../utils/ansi";

interface OutputPanelProps {
  projectRootPath: string | null;
}

function OutputLine({ line, idx }: { line: string; idx: number }) {
  return <AnsiLine line={line} idx={idx} className="output-panel-line" />;
}

export default function OutputPanel({ projectRootPath }: OutputPanelProps) {
  const { t } = useTranslation();
  const [lines, setLines] = useState<string[]>([]);
  const outputRef = useRef<HTMLDivElement>(null);
  const [autoScroll, setAutoScroll] = useState(true);

  useEffect(() => {
    if (!projectRootPath) { setLines([]); return; }
    const fetchOutput = async () => {
      try {
        const output = await GetProcessOutput(projectRootPath);
        setLines(output ?? []);
      } catch {}
    };
    fetchOutput();
    const interval = setInterval(fetchOutput, 500);
    return () => clearInterval(interval);
  }, [projectRootPath]);

  useEffect(() => {
    if (autoScroll && outputRef.current) {
      outputRef.current.scrollTop = outputRef.current.scrollHeight;
    }
  }, [lines, autoScroll]);

  if (!projectRootPath) {
    return (
      <div className="output-panel-empty">
        <ScrollText size={32} strokeWidth={1} />
        <span>{t("output.selectProject")}</span>
      </div>
    );
  }

  return (
    <div className="output-panel">
      <div className="output-panel-header">
        <ScrollText size={14} />
        <span>{t("output.projectOutput")}</span>
        <div className="output-panel-actions">
          <label className="output-panel-checkbox">
            <input type="checkbox" checked={autoScroll} onChange={(e) => setAutoScroll(e.target.checked)} />
            <span>{t("output.autoScroll")}</span>
          </label>
          <button className="output-panel-clear" onClick={() => setLines([])} title={t("output.clearOutput")}>
            <Trash2 size={14} />
          </button>
        </div>
      </div>
      <div className="output-panel-content" ref={outputRef}>
        {lines.length === 0 ? (
          <div className="output-panel-placeholder">{t("output.waiting")}</div>
        ) : (
          lines.map((line, i) => <OutputLine key={i} line={line} idx={i} />)
        )}
      </div>
    </div>
  );
}
