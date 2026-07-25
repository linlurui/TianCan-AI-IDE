import { useState, useEffect } from "react";
import { Cpu, HardDrive, Zap } from "lucide-react";

// Get current model status from router - more resilient to binding errors
async function getModelStatus() {
  try {
    // Use dynamic import to handle missing bindings gracefully
    const aiModule = await import("../bindings/ai");
    const ai = aiModule;
    if (typeof ai.GetRouterConfig === "function" && typeof ai.GetHardwareProfile === "function") {
      const [config, hw] = await Promise.all([ai.GetRouterConfig(), ai.GetHardwareProfile()]);
      if (!config || !hw) return null;
      return {
        currentTier: config.preferredTier || "unknown",
        availableModels: 0,
        hardware: hw,
        strategy: config.strategy,
        autoDowngrade: config.autoDowngrade,
      };
    }
    return null;
  } catch {
    return null;
  }
}

interface ModelStatusData {
  currentTier: string;
  availableModels: number;
  hardware: {
    hasGpu?: boolean;
    gpuMemoryGB?: number;
    gpuName?: string;
    availableRamGB?: number;
  };
  strategy?: string;
  autoDowngrade?: boolean;
}

export default function ModelStatusIndicator() {
  const [status, setStatus] = useState<ModelStatusData | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const fetchStatus = async () => {
      const s = await getModelStatus();
      setStatus(s);
      setLoading(false);
    };

    fetchStatus();
    // Refresh every 30 seconds
    const interval = setInterval(fetchStatus, 30000);
    return () => clearInterval(interval);
  }, []);

  const getTierColor = (tier: string) => {
    switch (tier) {
      case "local": return "#4ade80";  // Green
      case "api": return "#60a5fa";    // Blue
      case "web": return "#facc15";     // Yellow
      default: return "#9ca3af";        // Gray
    }
  };

  const getTierIcon = (tier: string) => {
    switch (tier) {
      case "local": return <HardDrive size={12} />;
      case "api": return <Cpu size={12} />;
      case "web": return <Zap size={12} />;
      default: return <Cpu size={12} />;
    }
  };

  const getTierLabel = (tier: string) => {
    switch (tier) {
      case "local": return "本地";
      case "api": return "API";
      case "web": return "Web";
      default: return "未知";
    }
  };

  if (loading) {
    return (
      <div className="model-status-indicator loading" title="加载中...">
        <div className="status-dot pulse" />
      </div>
    );
  }

  if (!status) {
    return null;
  }

  const tierColor = getTierColor(status.currentTier);
  const hw = status.hardware;

  return (
    <div
      className="model-status-indicator"
      title={`AI 模式: ${getTierLabel(status.currentTier)}${hw?.hasGpu ? ` | GPU: ${(hw.gpuMemoryGB || 0).toFixed(0)}GB` : ` | RAM: ${(hw?.availableRamGB || 0).toFixed(0)}GB 可用`}`}
    >
      <div
        className="status-dot"
        style={{ backgroundColor: tierColor }}
      />
      <span className="status-tier" style={{ color: tierColor }}>
        {getTierIcon(status.currentTier)}
        {getTierLabel(status.currentTier)}
      </span>
      {hw?.hasGpu && (
        <span className="status-badge gpu" title={`GPU: ${hw.gpuName || '可用'}`}>
          <Zap size={10} />
        </span>
      )}
    </div>
  );
}
