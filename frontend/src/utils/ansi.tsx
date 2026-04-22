import React from "react";

/* ── ANSI colour tables ───────────────────────────────────── */
const FG: Record<number, string> = {
  30: "#4e4e4e", 31: "#f85149", 32: "#4caf50", 33: "#e3b341",
  34: "#569cd6", 35: "#c678dd", 36: "#56b6c2", 37: "#abb2bf",
  90: "#666",    91: "#ff6b6b", 92: "#98c379", 93: "#e5c07b",
  94: "#61afef", 95: "#c678dd", 96: "#56b6c2", 97: "#dcdfe4",
};
const BG: Record<number, string> = {
  40: "#4e4e4e", 41: "#f85149", 42: "#4caf50", 43: "#e3b341",
  44: "#569cd6", 45: "#c678dd", 46: "#56b6c2", 47: "#abb2bf",
};

export interface AnsiSpan { text: string; color?: string; bg?: string; bold?: boolean; }

export function parseAnsi(raw: string): AnsiSpan[] {
  const spans: AnsiSpan[] = [];
  let cur: Omit<AnsiSpan, "text"> = {};
  let last = 0;
  const re = /\x1b\[([0-9;]*)m/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(raw)) !== null) {
    if (m.index > last) spans.push({ ...cur, text: raw.slice(last, m.index) });
    last = m.index + m[0].length;
    const ps = m[1] === "" ? [0] : m[1].split(";").map(Number);
    let next = { ...cur };
    for (const p of ps) {
      if (p === 0)                      { next = {}; }
      else if (p === 1)                 { next.bold = true; }
      else if (FG[p] !== undefined)     { next.color = FG[p]; }
      else if (BG[p] !== undefined)     { next.bg = BG[p]; }
    }
    cur = next;
  }
  if (last < raw.length) spans.push({ ...cur, text: raw.slice(last) });
  return spans;
}

const URL_RE = /(https?:\/\/[^\s\x1b\]'"<>）】。！？]+)/g;

export function AnsiSpanNode({ span, lineIdx, spanIdx }: { span: AnsiSpan; lineIdx: number; spanIdx: number }) {
  const style: React.CSSProperties = {};
  if (span.color) style.color = span.color;
  if (span.bg)    style.backgroundColor = span.bg;
  if (span.bold)  style.fontWeight = "bold";

  const parts = span.text.split(URL_RE);
  const children = parts.map((part, i) => {
    if (URL_RE.test(part)) {
      URL_RE.lastIndex = 0;
      return (
        <a key={i} href={part} target="_blank" rel="noreferrer"
          className="output-link" onClick={(e) => e.stopPropagation()}>
          {part}
        </a>
      );
    }
    URL_RE.lastIndex = 0;
    return part;
  });

  return <span key={`${lineIdx}-${spanIdx}`} style={style}>{children}</span>;
}

export function AnsiLine({ line, idx, className }: { line: string; idx: number; className?: string }) {
  const spans = parseAnsi(line);
  return (
    <div className={className}>
      {spans.map((s, si) => <AnsiSpanNode key={si} span={s} lineIdx={idx} spanIdx={si} />)}
    </div>
  );
}
