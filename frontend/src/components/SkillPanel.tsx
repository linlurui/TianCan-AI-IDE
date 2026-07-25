import React, { useState, useEffect, useCallback } from "react";
import {
  ListSkills, ValidateAllSkills, GetSkillDetail, DeleteSkill, InstallSkill,
  ExportSkill, ExportAllSkills, ImportSkillFromJSON, ImportSkillsFromFile,
  SearchMarketSkills, RateSkill,
} from "../../bindings/github.com/rocky233/tiancan-ai-ide/backend/ai/service";
import { MarketEntry } from "../../bindings/github.com/rocky233/tiancan-ai-ide/backend/skills/models";
import {
  Package, Trash2, Plus, CheckCircle, XCircle, RefreshCw, FileText,
  Download, Upload, Search, Star, Tag, X, ChevronDown, ChevronUp
} from "lucide-react";

interface SkillInfo {
  name: string;
  description: string;
  version: string;
  triggers: string[];
  stepCount: number;
  filePath: string;
}

interface SkillDetail {
  name: string;
  description: string;
  version: string;
  triggers: string[];
  steps: { tool: string; args: Record<string, unknown> }[];
  depends_on: string[];
  validated: boolean;
  validationMsg: string;
}

export default function SkillPanel() {
  const [skills, setSkills] = useState<SkillInfo[]>([]);
  const [validationResults, setValidationResults] = useState<Record<string, string>>({});
  const [selectedSkill, setSelectedSkill] = useState<SkillDetail | null>(null);
  const [showInstall, setShowInstall] = useState(false);
  const [installYaml, setInstallYaml] = useState("");
  const [installError, setInstallError] = useState("");
  const [loading, setLoading] = useState(false);

  // Market state
  const [showMarket, setShowMarket] = useState(false);
  const [marketQuery, setMarketQuery] = useState("");
  const [marketCategory, setMarketCategory] = useState("all");
  const [marketResults, setMarketResults] = useState<MarketEntry[]>([]);
  const [marketLoading, setMarketLoading] = useState(false);
  const [importingFile, setImportingFile] = useState<string>("");
  const [exportMessage, setExportMessage] = useState("");

  const refreshSkills = useCallback(async () => {
    setLoading(true);
    try {
      const list = await ListSkills();
      setSkills(list || []);
    } catch {
      setSkills([]);
    }
    setLoading(false);
  }, []);

  const refreshValidation = useCallback(async () => {
    try {
      const results = await ValidateAllSkills();
      setValidationResults((results as Record<string, string>) || {});
    } catch {
      setValidationResults({});
    }
  }, []);

  useEffect(() => {
    refreshSkills();
    refreshValidation();
  }, [refreshSkills, refreshValidation]);

  const handleSelectSkill = async (name: string) => {
    try {
      const detail = await GetSkillDetail(name);
      setSelectedSkill(detail as unknown as SkillDetail);
    } catch {
      setSelectedSkill(null);
    }
  };

  const handleDelete = async (name: string) => {
    if (!confirm(`确定删除技能 "${name}" 吗？`)) return;
    try {
      await DeleteSkill(name);
      setSelectedSkill(null);
      await refreshSkills();
      await refreshValidation();
    } catch (e: any) {
      alert(`删除失败: ${e?.message || e}`);
    }
  };

  const handleInstall = async () => {
    if (!installYaml.trim()) return;
    setInstallError("");
    try {
      await InstallSkill(installYaml);
      setShowInstall(false);
      setInstallYaml("");
      await refreshSkills();
      await refreshValidation();
    } catch (e: any) {
      setInstallError(e?.message || String(e));
    }
  };

  // Market functions
  const handleSearchMarket = async () => {
    setMarketLoading(true);
    try {
      const results = await SearchMarketSkills(marketQuery, marketCategory);
      setMarketResults(results || []);
    } catch {
      setMarketResults([]);
    }
    setMarketLoading(false);
  };

  const handleImportFromMarket = async (entry: MarketEntry) => {
    try {
      const result = await ImportSkillFromJSON(JSON.stringify(entry));
      if (result === "ok") {
        alert(`✅ 技能 "${entry.name}" 导入成功！`);
        await refreshSkills();
        await refreshValidation();
      } else {
        alert(`导入失败: ${result}`);
      }
    } catch (e: any) {
      alert(`导入失败: ${e?.message || e}`);
    }
  };

  const handleImportFromFile = async () => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = ".json,.yaml,.yml";
    input.onchange = async (e: any) => {
      const file = e.target.files?.[0];
      if (!file) return;
      const text = await file.text();
      try {
        let result: string = "ok";
        if (file.name.endsWith(".yaml") || file.name.endsWith(".yml")) {
          await InstallSkill(text);
        } else {
          result = await ImportSkillsFromFile(file.name);
        }
        if (result === "ok") {
          alert("✅ 导入成功！");
          await refreshSkills();
          await refreshValidation();
        } else {
          alert(`导入失败: ${result}`);
        }
      } catch (e: any) {
        alert(`导入失败: ${e?.message || e}`);
      }
    };
    input.click();
  };

  const handleExportSkill = async (name: string) => {
    try {
      const json = await ExportSkill(name);
      if (!json) {
        alert("导出失败");
        return;
      }
      const blob = new Blob([json], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `${name}.skill.json`;
      a.click();
      URL.revokeObjectURL(url);
      setExportMessage(`✅ 已导出 ${name}`);
      setTimeout(() => setExportMessage(""), 2000);
    } catch (e: any) {
      alert(`导出失败: ${e?.message || e}`);
    }
  };

  const handleExportAll = async () => {
    try {
      const json = await ExportAllSkills();
      if (!json || json === "[]") {
        alert("没有可导出的技能");
        return;
      }
      const blob = new Blob([json], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = "all-skills.skill.json";
      a.click();
      URL.revokeObjectURL(url);
      setExportMessage("✅ 已导出所有技能");
      setTimeout(() => setExportMessage(""), 2000);
    } catch (e: any) {
      alert(`导出失败: ${e?.message || e}`);
    }
  };

  const handleRateSkill = async (name: string, rating: number) => {
    try {
      const result = await RateSkill(name, rating);
      if (result === "ok") {
        setExportMessage(`⭐ 已为 ${name} 评分 ${rating} 星`);
        setTimeout(() => setExportMessage(""), 2000);
      }
    } catch (e: any) {
      alert(`评分失败: ${e?.message || e}`);
    }
  };

  const sampleYaml = `name: my-skill
description: 示例技能
version: "1.0"
triggers:
  - 示例
steps:
  - tool: read_file
    args:
      path: README.md
`;

  const categories = [
    { value: "all", label: "全部" },
    { value: "testing", label: "🧪 测试" },
    { value: "refactor", label: "🔧 重构" },
    { value: "debug", label: "🔍 调试" },
    { value: "deploy", label: "🚀 部署" },
    { value: "git", label: "📦 Git" },
    { value: "database", label: "💾 数据库" },
    { value: "api", label: "🌐 API" },
    { value: "docs", label: "📝 文档" },
    { value: "general", label: "⚙️ 通用" },
  ];

  return (
    <div style={{ padding: 12, fontSize: 13, height: "100%", overflow: "auto" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12 }}>
        <Package size={16} />
        <strong>技能管理</strong>
        <div style={{ flex: 1 }} />
        <button
          className="ai-install-btn"
          onClick={refreshSkills}
          style={{ padding: "2px 8px" }}
          title="刷新"
        >
          <RefreshCw size={12} />
        </button>
        <button
          className="ai-install-btn"
          onClick={() => setShowMarket(!showMarket)}
          style={{ padding: "2px 8px", background: showMarket ? "#1a3a5c" : undefined }}
          title="技能市场"
        >
          <Search size={12} /> 市场
        </button>
        <button
          className="ai-install-btn"
          onClick={() => { setShowInstall(true); setInstallYaml(sampleYaml); setInstallError(""); }}
          style={{ padding: "2px 8px" }}
          title="安装技能"
        >
          <Plus size={12} /> 安装
        </button>
      </div>

      {/* Market section */}
      {showMarket && (
        <div style={{ marginBottom: 12, border: "1px solid #4a9eff", borderRadius: 6, padding: 10, background: "#0d1a2a" }}>
          <div style={{ display: "flex", alignItems: "center", gap: 6, marginBottom: 8 }}>
            <Tag size={14} color="#4a9eff" />
            <strong style={{ color: "#4a9eff" }}>技能市场</strong>
            {exportMessage && <span style={{ color: "#4caf50", fontSize: 11 }}>{exportMessage}</span>}
          </div>

          {/* Search bar */}
          <div style={{ display: "flex", gap: 6, marginBottom: 8 }}>
            <input
              type="text"
              placeholder="搜索技能..."
              value={marketQuery}
              onChange={(e) => setMarketQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && handleSearchMarket()}
              style={{
                flex: 1, padding: "4px 8px", fontSize: 12,
                background: "#1a1a2e", color: "#e0e0e0",
                border: "1px solid #333", borderRadius: 4,
              }}
            />
            <select
              value={marketCategory}
              onChange={(e) => setMarketCategory(e.target.value)}
              style={{
                padding: "4px 8px", fontSize: 12,
                background: "#1a1a2e", color: "#e0e0e0",
                border: "1px solid #333", borderRadius: 4,
              }}
            >
              {categories.map((c) => (
                <option key={c.value} value={c.value}>{c.label}</option>
              ))}
            </select>
            <button className="ai-install-btn" onClick={handleSearchMarket} style={{ padding: "4px 10px" }}>
              <Search size={12} />
            </button>
          </div>

          {/* Import/Export buttons */}
          <div style={{ display: "flex", gap: 6, marginBottom: 8 }}>
            <button className="ai-install-btn" onClick={handleImportFromFile} style={{ padding: "4px 10px" }}>
              <Download size={12} /> 导入文件
            </button>
            <button className="ai-install-btn" onClick={handleExportAll} style={{ padding: "4px 10px" }}>
              <Upload size={12} /> 导出全部
            </button>
          </div>

          {/* Market results */}
          {marketLoading && <div style={{ color: "#888", fontSize: 11 }}>搜索中...</div>}
          {marketResults.length === 0 && !marketLoading && marketQuery && (
            <div style={{ color: "#666", fontSize: 11 }}>未找到匹配的技能（本地搜索）</div>
          )}
          {marketResults.map((entry, idx) => (
            <div key={idx} style={{
              padding: "8px 10px", marginBottom: 6,
              border: "1px solid #333", borderRadius: 6,
              background: "#1a1a2e",
            }}>
              <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
                <strong style={{ flex: 1 }}>{entry.name}</strong>
                <span style={{ fontSize: 10, color: "#888" }}>v{entry.version}</span>
                <span style={{ fontSize: 10, color: "#4a9eff" }}>{entry.category}</span>
                <button
                  className="ai-install-btn"
                  onClick={() => handleImportFromMarket(entry)}
                  style={{ padding: "2px 8px" }}
                  title="安装此技能"
                >
                  <Download size={12} />
                </button>
                <button
                  className="ai-install-btn"
                  onClick={() => handleExportSkill(entry.name)}
                  style={{ padding: "2px 8px" }}
                  title="导出此技能"
                >
                  <Upload size={12} />
                </button>
              </div>
              <div style={{ fontSize: 11, color: "#aaa", marginTop: 2 }}>{entry.description}</div>
              <div style={{ display: "flex", gap: 8, marginTop: 4, fontSize: 10, color: "#666" }}>
                <span>👤 {entry.author}</span>
                <span>📥 {entry.downloadCount || 0} 下载</span>
                {entry.rating > 0 && <span>⭐ {entry.rating.toFixed(1)}</span>}
                {entry.tags?.length > 0 && <span>标签: {entry.tags.join(", ")}</span>}
              </div>
              {/* Rating stars */}
              <div style={{ marginTop: 4 }}>
                {[1,2,3,4,5].map((star) => (
                  <button
                    key={star}
                    onClick={() => handleRateSkill(entry.name, star)}
                    style={{
                      background: "none", border: "none", cursor: "pointer",
                      padding: "0 2px", fontSize: 14,
                    }}
                    title={`评 ${star} 星`}
                  >
                    {entry.rating >= star ? "⭐" : "☆"}
                  </button>
                ))}
              </div>
            </div>
          ))}
        </div>
      )}

      {loading && <div style={{ color: "#888" }}>加载中...</div>}

      {/* Install dialog */}
      {showInstall && (
        <div style={{ marginBottom: 12, border: "1px solid #444", borderRadius: 6, padding: 10 }}>
          <div style={{ marginBottom: 6, fontWeight: 500 }}>
            <FileText size={12} /> 粘贴技能 YAML 内容：
          </div>
          <textarea
            value={installYaml}
            onChange={(e) => setInstallYaml(e.target.value)}
            style={{
              width: "100%", minHeight: 150, fontSize: 12,
              background: "#1a1a2e", color: "#e0e0e0",
              border: "1px solid #333", borderRadius: 4, padding: 8,
              fontFamily: "monospace",
            }}
          />
          {installError && <div style={{ color: "#e55", fontSize: 11, marginTop: 4 }}>❌ {installError}</div>}
          <div style={{ marginTop: 6, display: "flex", gap: 8 }}>
            <button className="ai-install-btn" onClick={handleInstall} style={{ padding: "4px 12px" }}>
              <CheckCircle size={12} /> 确认安装
            </button>
            <button className="ai-install-btn" onClick={() => setShowInstall(false)} style={{ padding: "4px 12px" }}>
              取消
            </button>
          </div>
        </div>
      )}

      {/* Skill list */}
      {skills.length === 0 && !loading && (
        <div style={{ color: "#888", textAlign: "center", padding: 20 }}>
          暂无已安装的技能<br />
          <span style={{ fontSize: 11 }}>点击"安装"按钮添加技能</span>
        </div>
      )}

      {skills.map((s) => {
        const vResult = validationResults[s.name] || "";
        const isValid = vResult.startsWith("✓");
        const isInvalid = vResult.startsWith("✗");
        const isSelected = selectedSkill?.name === s.name;

        return (
          <div
            key={s.name}
            onClick={() => handleSelectSkill(s.name)}
            style={{
              padding: "8px 10px", marginBottom: 6,
              border: `1px solid ${isSelected ? "#4a9eff" : "#333"}`,
              borderRadius: 6, cursor: "pointer",
              background: isSelected ? "#1a2a4a" : "#1a1a2e",
            }}
          >
            <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
              <strong style={{ flex: 1 }}>{s.name}</strong>
              <span style={{ fontSize: 10, color: "#888" }}>v{s.version}</span>
              {isValid && <span title={vResult}><CheckCircle size={12} color="#4caf50" /></span>}
              {isInvalid && <span title={vResult}><XCircle size={12} color="#e55" /></span>}
              <button
                onClick={(e) => { e.stopPropagation(); handleDelete(s.name); }}
                style={{ background: "none", border: "none", cursor: "pointer", padding: 2 }}
                title="删除"
              >
                <Trash2 size={12} color="#e55" />
              </button>
            </div>
            <div style={{ fontSize: 11, color: "#aaa", marginTop: 2 }}>{s.description}</div>
            <div style={{ fontSize: 10, color: "#666", marginTop: 2 }}>
              {s.stepCount} 步 {s.triggers?.length > 0 ? `| 触发: ${s.triggers.join(", ")}` : ""}
            </div>
          </div>
        );
      })}

      {/* Skill detail */}
      {selectedSkill && (
        <div style={{ marginTop: 12, borderTop: "1px solid #333", paddingTop: 10 }}>
          <h4 style={{ margin: "0 0 6px 0" }}>{selectedSkill.name}</h4>
          <div style={{ fontSize: 11, color: "#aaa" }}>{selectedSkill.description}</div>

          {selectedSkill.depends_on?.length > 0 && (
            <div style={{ fontSize: 11, marginTop: 6 }}>
              <strong>依赖:</strong> {selectedSkill.depends_on.join(", ")}
            </div>
          )}

          <div style={{ marginTop: 8 }}>
            <strong style={{ fontSize: 12 }}>步骤:</strong>
            {selectedSkill.steps?.map((step, i) => (
              <div key={i} style={{ fontSize: 11, marginLeft: 10, marginTop: 2 }}>
                {i + 1}. <code>{step.tool}</code>
                {Object.keys(step.args || {}).length > 0 && (
                  <span style={{ color: "#888" }}> {JSON.stringify(step.args)}</span>
                )}
              </div>
            ))}
          </div>

          <div style={{ marginTop: 8, fontSize: 11 }}>
            {selectedSkill.validated
              ? <span style={{ color: "#4caf50" }}>✓ 验证通过</span>
              : <span style={{ color: "#e55" }}>✗ {selectedSkill.validationMsg || "未验证"}</span>
            }
          </div>
        </div>
      )}
    </div>
  );
}
