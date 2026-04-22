import React from "react";
import { useTranslation } from "../i18n";
import { X } from "lucide-react";
import ExtensionPanel from "./ExtensionPanel";
import type { Extension } from "../bindings/extension";
import type * as Monaco from "monaco-editor";

interface Props {
  ext: Extension | null;
  lspPort: number;
  rootPath?: string;
  monaco?: typeof Monaco;
  onClose: () => void;
  onUninstall: (ext: Extension) => void;
}

export default function ExtensionDrawer({ ext, lspPort, rootPath, monaco, onClose, onUninstall }: Props) {
  const { t } = useTranslation();
  if (!ext) return null;

  return (
    <>
      <div className="ext-drawer-backdrop" onClick={onClose} />
      <div className="ext-drawer">
        <div className="ext-drawer-header">
          <span className="ext-drawer-title">{t("extensions.details")}</span>
          <button className="ext-drawer-close" onClick={onClose} title={t("extensions.close")}>
            <X size={15} />
          </button>
        </div>
        <div className="ext-drawer-body">
          <ExtensionPanel
            ext={ext}
            lspPort={lspPort}
            rootPath={rootPath}
            monaco={monaco}
            onUninstall={(e) => { onUninstall(e); onClose(); }}
          />
        </div>
      </div>
    </>
  );
}
