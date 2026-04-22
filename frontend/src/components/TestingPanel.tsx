import React from "react";
import { FlaskConical, Code2, ChevronRight } from "lucide-react";
import { useTranslation } from "../i18n";

interface Props {
  onOpenApiTest: () => void;
  onOpenPlaywright: () => void;
}

export default function TestingPanel({ onOpenApiTest, onOpenPlaywright }: Props) {
  const { t } = useTranslation();
  return (
    <div className="testing-panel">
      <div className="testing-panel-header">
        <FlaskConical size={13} className="testing-panel-icon" />
        <span className="testing-panel-title">{t("testing.title")}</span>
      </div>

      <div className="testing-group">
        <div className="testing-group-label">{t("testing.apiGroup")}</div>
        <div className="testing-item" onClick={onOpenApiTest}>
          <FlaskConical size={13} className="testing-item-icon api" />
          <div className="testing-item-info">
            <span className="testing-item-name">{t("testing.apiName")}</span>
            <span className="testing-item-desc">{t("testing.apiDesc")}</span>
          </div>
          <ChevronRight size={12} className="testing-item-arrow" />
        </div>
      </div>

      <div className="testing-group">
        <div className="testing-group-label">{t("testing.autoGroup")}</div>
        <div className="testing-item" onClick={onOpenPlaywright}>
          <Code2 size={13} className="testing-item-icon pw" />
          <div className="testing-item-info">
            <span className="testing-item-name">{t("testing.playwrightName")}</span>
            <span className="testing-item-desc">{t("testing.playwrightDesc")}</span>
          </div>
          <ChevronRight size={12} className="testing-item-arrow" />
        </div>
      </div>
    </div>
  );
}
