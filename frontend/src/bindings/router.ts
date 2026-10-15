// Router bindings - connects to backend router package

// eslint-disable-next-line @typescript-eslint/ban-ts-comment
// @ts-ignore: Unused imports
import { Call as $Call, Create as $Create } from "/wails/runtime.js";
// eslint-disable-next-line @typescript-eslint/ban-ts-comment
// @ts-ignore: Unused imports
import * as router$0 from "../../bindings/github.com/linlurui/tiancan-ai-ide/backend/router/models";

export type { HardwareProfile, SmartRouterConfig, TierRecommendation } from "../../bindings/github.com/linlurui/tiancan-ai-ide/backend/router/models";

// Hardware monitor hooks for real-time monitoring
export interface HardwareMonitorState {
  cpuPercent: number;
  memoryPercent: number;
  gpuUtilization?: number;
  lastUpdate: number;
}

export type HardwareUpdateCallback = (state: HardwareMonitorState) => void;

export interface ModelStatus {
  currentTier: "local" | "api" | "web" | "unknown";
  availableModels: number;
  hardware: router$0.HardwareProfile;
  strategy?: string;
  autoDowngrade?: boolean;
}

// Get current router/system status
export async function GetRouterStatus(): Promise<ModelStatus | null> {
  try {
    // Use GetRouterConfig and GetHardwareProfile from ai service
    const { GetRouterConfig, GetHardwareProfile } = await import("./ai");
    const [config, hw] = await Promise.all([GetRouterConfig(), GetHardwareProfile()]);

    if (!config || !hw) return null;

    return {
      currentTier: config.preferredTier as any || "unknown",
      availableModels: 0, // Will be updated by ListModelsAvailable
      hardware: hw,
      strategy: config.strategy,
      autoDowngrade: config.autoDowngrade,
    };
  } catch (e) {
    console.warn("GetRouterStatus failed:", e);
    return null;
  }
}

// Route a message and get tier recommendation
export async function RouteMessage(
  message: string,
  providerScores?: Record<string, number>
): Promise<router$0.TierRecommendation | null> {
  try {
    const { RouteRequest } = await import("./ai");
    return await RouteRequest(message);
  } catch (e) {
    console.warn("RouteMessage failed:", e);
    return null;
  }
}

// Get router config
export async function GetRouterConfig(): Promise<router$0.SmartRouterConfig | null> {
  try {
    const { GetRouterConfig } = await import("./ai");
    return await GetRouterConfig();
  } catch (e) {
    console.warn("GetRouterConfig failed:", e);
    return null;
  }
}

// Hardware monitor hook for real-time monitoring
let hardwareUpdateCallback: HardwareUpdateCallback | null = null;
let hardwareMonitorInterval: ReturnType<typeof setInterval> | null = null;

export function useHardwareMonitor(callback: HardwareUpdateCallback): () => void {
  hardwareUpdateCallback = callback;

  const updateHardwareState = async () => {
    try {
      const { GetHardwareProfile } = await import("./ai");
      const hw = await GetHardwareProfile();
      if (hw && hardwareUpdateCallback) {
        hardwareUpdateCallback({
          cpuPercent: hw.cpuPercent,
          memoryPercent: hw.usedRamPercent,
          gpuUtilization: (hw as any).gpuUtilization,
          lastUpdate: Date.now(),
        });
      }
    } catch (e) {
      console.warn("Hardware monitor update failed:", e);
    }
  };

  // Initial update
  updateHardwareState();

  // Set up interval for continuous monitoring
  if (!hardwareMonitorInterval) {
    hardwareMonitorInterval = setInterval(updateHardwareState, 5000); // Update every 5 seconds
  }

  // Return cleanup function
  return () => {
    hardwareUpdateCallback = null;
    if (hardwareMonitorInterval) {
      clearInterval(hardwareMonitorInterval);
      hardwareMonitorInterval = null;
    }
  };
}

// Get recommended model tier based on task complexity
export async function GetRecommendedTier(
  message: string,
  complexity?: "low" | "medium" | "high" | "expert"
): Promise<router$0.TierRecommendation | null> {
  try {
    // Try to call RouteWithComplexity if available, otherwise fall back to RouteMessage
    const ai = await import("./ai");
    if (complexity && typeof (ai as any).RouteWithComplexity === 'function') {
      return await (ai as any).RouteWithComplexity(message, complexity);
    }
    // Fallback to simple routing
    return await RouteMessage(message);
  } catch (e) {
    console.warn("GetRecommendedTier failed:", e);
    return null;
  }
}

// Set router config
export async function SetRouterConfig(cfg: router$0.SmartRouterConfig): Promise<void> {
  try {
    const { SetRouterConfig } = await import("./ai");
    await SetRouterConfig(cfg);
  } catch (e) {
    console.warn("SetRouterConfig failed:", e);
  }
}

// Record a routing failure
export async function RecordRoutingFailure(tier: string, error: string): Promise<void> {
  try {
    const { RecordFailure } = await import("./ai");
    await RecordFailure(tier, error);
  } catch (e) {
    console.warn("RecordRoutingFailure failed:", e);
  }
}
