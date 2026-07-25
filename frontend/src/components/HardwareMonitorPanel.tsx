import { useState, useEffect } from "react";
import { Cpu, MemoryStick, Monitor } from "lucide-react";

export interface HardwareMonitorPanelProps {
  refreshInterval?: number; // milliseconds, default 5000
}

interface HardwareStatus {
  cpuCores: number;
  cpuPercent: number;
  totalRamGB: number;
  availableRamGB: number;
  usedRamPercent: number;
  hasGPU: boolean;
  gpuMemoryGB?: number;
  gpuUtilization?: number;
  gpuName?: string;
  os: string;
}

// Mock data - in real app would come from backend
const mockHardware: HardwareStatus = {
  cpuCores: 10,
  cpuPercent: 45.2,
  totalRamGB: 32,
  availableRamGB: 16.5,
  usedRamPercent: 48.4,
  hasGPU: true,
  gpuMemoryGB: 8,
  gpuUtilization: 32,
  gpuName: "NVIDIA GeForce RTX 3080",
  os: "darwin",
};

export function HardwareMonitorPanel({ refreshInterval = 5000 }: HardwareMonitorPanelProps) {
  const [hw, setHw] = useState<HardwareStatus>(mockHardware);
  const [lastUpdate, setLastUpdate] = useState<Date>(new Date());

  // In real implementation, would poll from backend
  useEffect(() => {
    const interval = setInterval(() => {
      // Simulate slight variations in usage
      setHw(prev => ({
        ...prev,
        cpuPercent: Math.min(100, Math.max(0, prev.cpuPercent + (Math.random() - 0.5) * 10)),
        usedRamPercent: Math.min(100, Math.max(0, prev.usedRamPercent + (Math.random() - 0.5) * 2)),
        gpuUtilization: prev.hasGPU ? Math.min(100, Math.max(0, (prev.gpuUtilization || 0) + (Math.random() - 0.5) * 15)) : undefined,
        availableRamGB: prev.totalRamGB * (1 - (prev.usedRamPercent / 100)),
      }));
      setLastUpdate(new Date());
    }, refreshInterval);

    return () => clearInterval(interval);
  }, [refreshInterval]);

  const getResourceStatusColor = (percent: number) => {
    if (percent >= 90) return "text-red-500";
    if (percent >= 75) return "text-yellow-500";
    return "text-green-500";
  };

  const getResourceBgColor = (percent: number) => {
    if (percent >= 90) return "bg-red-500";
    if (percent >= 75) return "bg-yellow-500";
    return "bg-green-500";
  };

  const formatTime = (date: Date) => {
    return date.toLocaleTimeString();
  };

  return (
    <div className="bg-white dark:bg-gray-800 rounded-lg shadow-lg p-4 min-w-[280px]">
      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-2">
          <Monitor className="w-5 h-5 text-blue-500" />
          <h3 className="font-semibold text-gray-900 dark:text-gray-100">Hardware Status</h3>
        </div>
        <span className="text-xs text-gray-400">
          Updated {formatTime(lastUpdate)}
        </span>
      </div>

      {/* CPU Section */}
      <div className="mb-4">
        <div className="flex items-center gap-2 mb-2">
          <Cpu className="w-4 h-4 text-gray-400" />
          <span className="text-sm font-medium text-gray-700 dark:text-gray-300">CPU</span>
          <span className="ml-auto text-sm font-semibold text-gray-900 dark:text-gray-100">
            {hw.cpuCores} cores
          </span>
        </div>
        <div className="relative h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
          <div
            className={`absolute left-0 top-0 h-full transition-all ${getResourceBgColor(hw.cpuPercent)}`}
            style={{ width: `${hw.cpuPercent}%` }}
          />
        </div>
        <div className="flex justify-between text-xs text-gray-500 mt-1">
          <span>Usage</span>
          <span className={`font-medium ${getResourceStatusColor(hw.cpuPercent)}`}>
            {hw.cpuPercent.toFixed(1)}%
          </span>
        </div>
      </div>

      {/* Memory Section */}
      <div className="mb-4">
        <div className="flex items-center gap-2 mb-2">
          <MemoryStick className="w-4 h-4 text-gray-400" />
          <span className="text-sm font-medium text-gray-700 dark:text-gray-300">Memory</span>
          <span className="ml-auto text-sm font-semibold text-gray-900 dark:text-gray-100">
            {(hw.totalRamGB - hw.availableRamGB).toFixed(1)} / {hw.totalRamGB.toFixed(0)} GB
          </span>
        </div>
        <div className="relative h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
          <div
            className={`absolute left-0 top-0 h-full transition-all ${getResourceBgColor(hw.usedRamPercent)}`}
            style={{ width: `${hw.usedRamPercent}%` }}
          />
        </div>
        <div className="flex justify-between text-xs text-gray-500 mt-1">
          <span>Used</span>
          <span className={`font-medium ${getResourceStatusColor(hw.usedRamPercent)}`}>
            {hw.usedRamPercent.toFixed(1)}%
          </span>
        </div>
        <div className="flex justify-between text-xs text-gray-500 mt-1">
          <span>Available</span>
          <span className="text-gray-400">
            {hw.availableRamGB.toFixed(1)} GB
          </span>
        </div>
      </div>

      {/* GPU Section */}
      {hw.hasGPU && (
        <div className="mb-4">
          <div className="flex items-center gap-2 mb-2">
            <Monitor className="w-4 h-4 text-gray-400" />
            <span className="text-sm font-medium text-gray-700 dark:text-gray-300">GPU</span>
            <span className="ml-auto text-xs text-gray-500">
              {hw.gpuName || "Unknown"}
            </span>
          </div>
          {hw.gpuMemoryGB && (
            <div className="text-xs text-gray-500 mb-2">
              VRAM: {hw.gpuMemoryGB} GB
            </div>
          )}
          {hw.gpuUtilization !== undefined && (
            <>
              <div className="relative h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
                <div
                  className={`absolute left-0 top-0 h-full transition-all ${getResourceBgColor(hw.gpuUtilization)}`}
                  style={{ width: `${hw.gpuUtilization}%` }}
                />
              </div>
              <div className="flex justify-between text-xs text-gray-500 mt-1">
                <span>GPU Utilization</span>
                <span className={`font-medium ${getResourceStatusColor(hw.gpuUtilization)}`}>
                  {hw.gpuUtilization.toFixed(1)}%
                </span>
              </div>
            </>
          )}
        </div>
      )}

      {/* Recommendations */}
      <div className="mt-4 p-3 bg-gray-50 dark:bg-gray-700/50 rounded-lg">
        <h4 className="text-xs font-medium text-gray-500 dark:text-gray-400 mb-2">Recommendations</h4>
        <ul className="text-xs text-gray-600 dark:text-gray-300 space-y-1">
          {hw.usedRamPercent >= 85 && (
            <li className="flex items-start gap-1">
              <span className="text-red-500">●</span>
              High memory usage - consider closing unused applications
            </li>
          )}
          {hw.cpuPercent >= 80 && (
            <li className="flex items-start gap-1">
              <span className="text-yellow-500">●</span>
              High CPU load - AI operations may be slower
            </li>
          )}
          {hw.hasGPU && (hw.gpuUtilization || 0) >= 90 && (
            <li className="flex items-start gap-1">
              <span className="text-orange-500">●</span>
              GPU heavily utilized - local AI models may queue
            </li>
          )}
          {hw.usedRamPercent < 70 && hw.cpuPercent < 60 && (!hw.hasGPU || (hw.gpuUtilization || 0) < 50) && (
            <li className="flex items-start gap-1">
              <span className="text-green-500">●</span>
              System resources available - good for local AI models
            </li>
          )}
        </ul>
      </div>
    </div>
  );
}
