import { useState, useEffect } from "react";
import { RefreshCw, CheckCircle2, XCircle, TrendingDown, AlertTriangle } from "lucide-react";

export interface AutoResearchProgressProps {
  agentId: string;
  onComplete?: (result: AutoResearchResult) => void;
  onCancel?: () => void;
}

export interface AutoResearchResult {
  success: boolean;
  agentId: string;
  target: string;
  metric: string;
  baseline: number;
  bestValue: number;
  improvement: number;
  iterations: number;
  bestHypothesis: string;
  changes: CodeChange[];
  warnings: string[];
  history: IterationRecord[];
}

export interface CodeChange {
  iteration: number;
  hypothesis: string;
  beforeSize: number;
  afterSize: number;
  changePercent: number;
  resultMs: number;
  improved: boolean;
}

export interface IterationRecord {
  iteration: number;
  hypothesis: string;
  resultMs: number;
  isImprovement: boolean;
  changeSize: number;
  durationMs: number;
}

export interface ProgressUpdate {
  agentId?: string;
  phase: "baseline" | "hypothesis" | "iteration" | "verification" | "complete" | "error";
  iteration?: number;
  totalIters?: number;
  message: string;
  progress?: number;
  baselineMs?: number;
  resultMs?: number;
  improvement?: number;
  target?: string;
  metric?: string;
}

// Mock progress updates for demo (in real app, would subscribe to backend events)
const mockProgressUpdates: ProgressUpdate[] = [
  { phase: "baseline", message: "Measuring baseline performance...", progress: 0 },
  { phase: "baseline", message: "Baseline: 125.5ms", progress: 10, baselineMs: 125.5 },
  { phase: "iteration", iteration: 1, totalIters: 5, message: "Testing optimization iteration 1...", progress: 20, baselineMs: 125.5 },
  { phase: "iteration", iteration: 1, message: "Result: 118.2ms (5.8% improvement)", progress: 30, resultMs: 118.2, improvement: 5.8, baselineMs: 125.5 },
  { phase: "iteration", iteration: 2, totalIters: 5, message: "Testing optimization iteration 2...", progress: 40, baselineMs: 118.2 },
  { phase: "iteration", iteration: 2, message: "Result: 112.1ms (10.7% improvement)", progress: 50, resultMs: 112.1, improvement: 10.7, baselineMs: 118.2 },
  { phase: "iteration", iteration: 3, totalIters: 5, message: "Testing optimization iteration 3...", progress: 60, baselineMs: 112.1 },
  { phase: "iteration", iteration: 3, message: "Result: 109.8ms (12.5% improvement)", progress: 70, resultMs: 109.8, improvement: 12.5, baselineMs: 112.1 },
  { phase: "complete", message: "AutoResearch complete!", progress: 100, resultMs: 109.8, improvement: 12.5, baselineMs: 125.5 },
];

export function AutoResearchProgress({ agentId, onComplete, onCancel }: AutoResearchProgressProps) {
  const [progress, setProgress] = useState<ProgressUpdate>({ phase: "baseline", message: "Initializing...", progress: 0 });
  const [history, setHistory] = useState<{ iteration: number; ms: number; improved: boolean }[]>([]);
  const [isRunning, setIsRunning] = useState(true);
  const [currentHypothesis, setCurrentHypothesis] = useState<string>("");

  useEffect(() => {
    // Subscribe to backend events
    const handleStart = (data: { agentId: string; target: string; metric: string; totalIters: number }) => {
      setIsRunning(true);
      setProgress({
        phase: "baseline",
        message: "正在测量基线性能...",
        progress: 5,
        target: data.target,
        metric: data.metric,
      });
    };

    const handleProgress = (data: ProgressUpdate) => {
      setProgress(data);

      if (data.phase === "iteration" && data.iteration && data.resultMs !== undefined) {
        setHistory(prev => [...prev, {
          iteration: data.iteration as number,
          ms: data.resultMs as number,
          improved: data.improvement !== undefined && data.improvement > 0,
        }]);
        if (data.message.includes("Refined hypothesis")) {
          setCurrentHypothesis(data.message.replace("Refined hypothesis: ", ""));
        }
      }

      if (data.phase === "complete") {
        setIsRunning(false);
        setProgress({ ...data, progress: 100 });
        if (onComplete) {
          onComplete({
            success: true,
            agentId: data.agentId || agentId,
            target: data.target || "",
            metric: data.metric || "execution_time",
            baseline: data.baselineMs || 0,
            bestValue: data.resultMs || 0,
            improvement: data.improvement || 0,
            iterations: data.iteration || 0,
            bestHypothesis: currentHypothesis,
            changes: [],
            warnings: [],
            history: history.map(h => ({
              iteration: h.iteration,
              hypothesis: currentHypothesis,
              resultMs: h.ms,
              isImprovement: h.improved,
              changeSize: 0,
              durationMs: 0,
            })),
          });
        }
      }

      if (data.phase === "error") {
        setIsRunning(false);
        if (onComplete) {
          onComplete({
            success: false,
            agentId: agentId,
            target: data.target || "",
            metric: data.metric || "",
            baseline: 0,
            bestValue: 0,
            improvement: 0,
            iterations: 0,
            bestHypothesis: "",
            changes: [],
            warnings: [data.message],
            history: [],
          });
        }
      }
    };

    // Use mock progress updates for demo
    let index = 0;
    const interval = setInterval(() => {
      if (index < mockProgressUpdates.length) {
        const update = mockProgressUpdates[index];
        setProgress(update);
        if (update.phase === "iteration" && update.iteration) {
          const ms = (update as any).currentMs || update.baselineMs || 100;
          setHistory(prev => [...prev, {
            iteration: (update.iteration as number),
            ms: ms as number,
            improved: update.improvement !== undefined && update.improvement > 0,
          }]);
        }
        if (update.phase === "complete") {
          setIsRunning(false);
          clearInterval(interval);
        }
        index++;
      }
    }, 2000);
    return () => clearInterval(interval);
  }, [agentId, onComplete]);

  const getPhaseIcon = () => {
    switch (progress.phase) {
      case "baseline":
        return <RefreshCw className="w-4 h-4 animate-spin text-blue-500" />;
      case "iteration":
        return <TrendingDown className="w-4 h-4 text-purple-500" />;
      case "complete":
        return progress.progress === 100 ? (
          <CheckCircle2 className="w-4 h-4 text-green-500" />
        ) : (
          <AlertTriangle className="w-4 h-4 text-yellow-500" />
        );
      case "error":
        return <XCircle className="w-4 h-4 text-red-500" />;
      default:
        return null;
    }
  };

  return (
    <div className="bg-white dark:bg-gray-800 rounded-lg shadow-lg p-4 min-w-[400px]">
      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-2">
          <RefreshCw className="w-5 h-5 text-purple-500" />
          <h3 className="font-semibold text-gray-900 dark:text-gray-100">AutoResearch Progress</h3>
        </div>
        <div className="flex items-center gap-2">
          {getPhaseIcon()}
          <span className="text-sm text-gray-500">
            {progress.phase === "iteration" && progress.iteration
              ? `Iteration ${progress.iteration}/${progress.totalIters || 5}`
              : progress.phase === "complete"
              ? "Complete"
              : progress.phase === "error"
              ? "Error"
              : "Running..."}
          </span>
        </div>
      </div>

      {/* Progress bar */}
      <div className="w-full bg-gray-200 dark:bg-gray-700 rounded-full h-2 mb-4">
        <div
          className={`h-2 rounded-full transition-all duration-500 ${
            progress.phase === "error"
              ? "bg-red-500"
              : progress.phase === "complete"
              ? "bg-green-500"
              : "bg-purple-500"
          }`}
          style={{ width: `${progress.progress || 0}%` }}
        />
      </div>

      {/* Current status message */}
      <div className="mb-4 p-3 bg-gray-50 dark:bg-gray-700/50 rounded-lg">
        <p className="text-sm text-gray-700 dark:text-gray-300">{progress.message}</p>
        {progress.iteration && (
          <p className="text-xs text-gray-500 mt-1">
            Iteration {progress.iteration} of {progress.totalIters || 5}
          </p>
        )}
      </div>

      {/* History chart */}
      {history.length > 0 && (
        <div className="mb-4">
          <h4 className="text-xs font-medium text-gray-500 dark:text-gray-400 mb-2">Performance History</h4>
          <div className="flex items-end gap-1 h-16">
            {history.map((entry, idx) => (
              <div
                key={idx}
                className={`flex-1 rounded-t transition-all ${
                  entry.improved ? "bg-green-500" : "bg-red-500"
                }`}
                style={{
                  height: `${Math.max(10, 100 - (entry.ms / (history[0]?.ms || 200)) * 100)}%`,
                }}
                title={`Iter ${entry.iteration}: ${entry.ms.toFixed(1)}ms`}
              />
            ))}
          </div>
          <div className="flex justify-between text-xs text-gray-400 mt-1">
            <span>Start: {history[0]?.ms.toFixed(1)}ms</span>
            {history.length > 1 && (
              <span className="text-green-500">
                Current: {history[history.length - 1]?.ms.toFixed(1)}ms
              </span>
            )}
          </div>
        </div>
      )}

      {/* Actions */}
      <div className="flex justify-end gap-2">
        {isRunning && onCancel && (
          <button
            onClick={onCancel}
            className="px-3 py-1.5 text-sm text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-700 rounded"
          >
            Cancel
          </button>
        )}
        {!isRunning && (
          <button
            onClick={() => onComplete?.({
              success: progress.phase === "complete",
              agentId: agentId,
              target: "example.go",
              metric: "execution_time",
              baseline: progress.baselineMs || 125.5,
              bestValue: history[history.length - 1]?.ms || progress.resultMs || 125.5,
              improvement: progress.improvement || 0,
              iterations: history.length,
              bestHypothesis: "Reduced memory allocations",
              changes: [],
              warnings: [],
              history: history.map((h, i) => ({
                iteration: h.iteration,
                hypothesis: "Memory optimization",
                resultMs: h.ms,
                isImprovement: h.improved,
                changeSize: 0,
                durationMs: 0,
              })),
            })}
            className="px-3 py-1.5 text-sm bg-purple-500 text-white rounded hover:bg-purple-600"
          >
            View Report
          </button>
        )}
      </div>
    </div>
  );
}
