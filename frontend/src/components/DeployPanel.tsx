import React from "react";
import { useTranslation } from "../i18n";
import { Rocket, Container, Box, Server, ChevronRight } from "lucide-react";

interface Props {
  onOpenDeploy: () => void;
}

export default function DeployPanel({ onOpenDeploy }: Props) {
  const { t } = useTranslation();
  return (
    <div className="testing-panel">
      <div className="testing-panel-header">
        <Rocket size={13} className="testing-panel-icon" style={{ color: "#e5c07b" }} />
        <span className="testing-panel-title">{t("deploy.title")}</span>
      </div>

      <div className="testing-group">
        <div className="testing-group-label">{t("deploy.management")}</div>
        <div className="testing-item" onClick={onOpenDeploy}>
          <Rocket size={13} className="testing-item-icon deploy" />
          <div className="testing-item-info">
            <span className="testing-item-name">{t("deploy.console")}</span>
            <span className="testing-item-desc">{t("deploy.consoleDesc")}</span>
          </div>
          <ChevronRight size={12} className="testing-item-arrow" />
        </div>
      </div>

      <div className="testing-group">
        <div className="testing-group-label">{t("deploy.orchestration")}</div>
        <div className="dep-feature-list">
          <div className="dep-feature-item"><Server size={11} /><span>{t("deploy.shellDeploy")}</span></div>
          <div className="dep-feature-item"><Container size={11} /><span>{t("deploy.dockerDeploy")}</span></div>
          <div className="dep-feature-item"><Box size={11} /><span>{t("deploy.k8sDeploy")}</span></div>
        </div>
      </div>
    </div>
  );
}
