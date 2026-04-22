import React from "react";
import { useTranslation } from "../i18n";
import { AlertTriangle, Check, X } from "lucide-react";

export interface PermissionRequest {
  id: string;
  toolName: string;
  args: string; // JSON string of tool args
  reason: string;
}

interface PermissionConfirmModalProps {
  request: PermissionRequest | null;
  onRespond: (requestID: string, approved: boolean) => void;
}

export default function PermissionConfirmModal({ request, onRespond }: PermissionConfirmModalProps) {
  const { t } = useTranslation();

  if (!request) return null;

  let parsedArgs: Record<string, unknown> = {};
  try {
    parsedArgs = JSON.parse(request.args);
  } catch { /* ignore */ }

  const isFileWrite = request.toolName === "write_file" || request.toolName === "create_file";
  const filePath = (parsedArgs.path as string) || (parsedArgs.filePath as string) || "";

  return (
    <div className="modal-overlay" style={{ zIndex: 10000 }}>
      <div className="modal" style={{ maxWidth: 520 }}>
        <div className="modal-header">
          <AlertTriangle size={18} style={{ color: "var(--warning, #e5a00d)" }} />
          <span className="modal-title">{t("permission.title")}</span>
        </div>

        <div className="modal-body">
          <p style={{ fontSize: 13, color: "var(--text-secondary)", marginBottom: 12 }}>
            {request.reason || t("permission.defaultReason")}
          </p>

          <div style={{
            background: "var(--bg-primary)",
            border: "1px solid var(--border-light)",
            borderRadius: 4,
            padding: 10,
            fontFamily: "var(--font-mono)",
            fontSize: 12,
            marginBottom: 12,
          }}>
            <div style={{ marginBottom: 6 }}>
              <span style={{ color: "var(--accent)" }}>{t("permission.tool")}: </span>
              <span>{request.toolName}</span>
            </div>
            {filePath && (
              <div style={{ marginBottom: 6 }}>
                <span style={{ color: "var(--accent)" }}>{t("permission.path")}: </span>
                <span>{filePath}</span>
              </div>
            )}
            <pre style={{ margin: 0, whiteSpace: "pre-wrap", wordBreak: "break-word", color: "var(--text-secondary)" }}>
              {(() => { try { return JSON.stringify(parsedArgs, null, 2); } catch { return request.args; } })()}
            </pre>
          </div>

          {isFileWrite && (
            <p style={{ fontSize: 11, color: "var(--warning, #e5a00d)", margin: 0 }}>
              {t("permission.fileWriteWarning")}
            </p>
          )}
        </div>

        <div className="modal-footer">
          <button className="btn btn-secondary" onClick={() => onRespond(request.id, false)}>
            <X size={14} /> {t("permission.deny")}
          </button>
          <button className="btn btn-primary" onClick={() => onRespond(request.id, true)}>
            <Check size={14} /> {t("permission.approve")}
          </button>
        </div>
      </div>
    </div>
  );
}
