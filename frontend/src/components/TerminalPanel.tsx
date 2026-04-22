import { useEffect, useRef } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { useTranslation } from "../i18n";

interface Props {
  port: number;
  active: boolean;
  hidden?: boolean;
  initCmd?: string; // command to run once when the terminal WebSocket first opens
}

export default function TerminalPanel({ port, active, hidden, initCmd }: Props) {
  const { t, i18n } = useTranslation();
  const containerRef = useRef<HTMLDivElement>(null);
  const fitRef = useRef<FitAddon | null>(null);

  useEffect(() => {
    if (!containerRef.current || port === 0) return;

    const term = new XTerm({
      theme: {
        background: "#1e1e1e",
        foreground: "#d4d4d4",
        cursor: "#569cd6",
        cursorAccent: "#0d0d0d",
        selectionBackground: "rgba(86,156,214,0.3)",
        black: "#1e1e1e",    red: "#f44747",
        green: "#6a9955",    yellow: "#d7ba7d",
        blue: "#569cd6",     magenta: "#c678dd",
        cyan: "#4ec9b0",     white: "#d4d4d4",
        brightBlack: "#808080",  brightRed: "#f44747",
        brightGreen: "#b5cea8",  brightYellow: "#dcdcaa",
        brightBlue: "#9cdcfe",   brightMagenta: "#c678dd",
        brightCyan: "#4ec9b0",   brightWhite: "#d4d4d4",
      },
      fontFamily: "Menlo, 'SF Mono', Monaco, 'Cascadia Code', Consolas, monospace",
      fontSize: 13,
      lineHeight: 1.35,
      cursorStyle: "block",
      cursorBlink: true,
      scrollback: 5000,
      allowTransparency: true,
    });

    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(containerRef.current);
    requestAnimationFrame(() => fit.fit());
    fitRef.current = fit;

    const ws = new WebSocket(`ws://127.0.0.1:${port}/pty?lang=${i18n.language}`);
    ws.binaryType = "arraybuffer";

    ws.onopen = () => {
      ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      ws.send(JSON.stringify({ type: "setlang", lang: i18n.language }));
      // Send initial command after a short delay to let the shell prompt appear
      if (initCmd) {
        setTimeout(() => {
          if (ws.readyState === WebSocket.OPEN) {
            ws.send(initCmd + "\r");
          }
        }, 600);
      }
    };
    ws.onmessage = (e) => {
      term.write(new Uint8Array(e.data as ArrayBuffer));
    };
    ws.onclose = () => {
      term.write(`\r\n\x1b[31m[${t("terminal.disconnected")}]\x1b[0m\r\n`);
    };
    ws.onerror = () => {
      term.write(`\r\n\x1b[31m[${t("terminal.wsError")}]\x1b[0m\r\n`);
    };

    term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(data);
    });

    const ro = new ResizeObserver(() => {
      fit.fit();
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      }
    });
    ro.observe(containerRef.current);

    return () => {
      ws.close();
      term.dispose();
      ro.disconnect();
      fitRef.current = null;
    };
  }, [port]);

  useEffect(() => {
    if (active && fitRef.current) {
      const id = setTimeout(() => fitRef.current?.fit(), 60);
      return () => clearTimeout(id);
    }
  }, [active]);

  if (port === 0) {
    return (
      <div style={{ display: "flex", alignItems: "center", justifyContent: "center", height: "100%", color: "var(--text-muted)", fontSize: 12 }}>
        {t("terminal.starting")}
      </div>
    );
  }

  return (
    <div
      ref={containerRef}
      style={{
        width: "100%",
        height: "100%",
        padding: "4px 6px",
        boxSizing: "border-box",
        overflow: "hidden",
        display: hidden ? "none" : "block",
      }}
    />
  );
}
