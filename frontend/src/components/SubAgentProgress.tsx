import React, { useEffect, useState, useRef } from "react";
import { Events } from "@wailsio/runtime";
import { Wrench, CheckCircle2, XCircle, Loader2, ChevronDown, ChevronRight } from "lucide-react";

interface SubAgentStep {
  agentId: string;
  agentType: string;
  iteration: number;
  toolName?: string;
  toolArgs?: Record<string, unknown>;
  output?: string;
  isDone: boolean;
  error?: string;
}

interface AgentRun {
  agentId: string;
  agentType: string;
  steps: SubAgentStep[];
  isDone: boolean;
  error?: string;
}

export default function SubAgentProgressPanel() {
  const [runs, setRuns] = useState<AgentRun[]>([]);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const off = Events.On("subagent:progress", (evt: any) => {
      const data = evt?.data ?? evt;
      if (!data || !data.agentId) return;

      setRuns((prev) => {
        const existing = prev.findIndex((r) => r.agentId === data.agentId);
        if (existing >= 0) {
          // Update existing run
          const updated = [...prev];
          const run = { ...updated[existing], steps: [...updated[existing].steps] };

          // Find or add step for this iteration
          const stepIdx = run.steps.findIndex((s) => s.iteration === data.iteration);
          if (stepIdx >= 0) {
            run.steps[stepIdx] = { ...run.steps[stepIdx], ...data };
          } else {
            run.steps.push(data as SubAgentStep);
          }

          if (data.isDone) {
            run.isDone = true;
            run.error = data.error;
          }
          updated[existing] = run;
          return updated;
        } else {
          // New agent run
          return [...prev, {
            agentId: data.agentId,
            agentType: data.agentType || "general-purpose",
            steps: [data as SubAgentStep],
            isDone: data.isDone || false,
            error: data.error,
          }];
        }
      });

      // Auto-expand new runs
      setExpanded((prev) => ({
        ...prev,
        [data.agentId]: prev[data.agentId] ?? true,
      }));
    });
    return () => { off(); };
  }, []);

  // Auto-scroll to bottom
  useEffect(() => {
    if (panelRef.current) {
      panelRef.current.scrollTop = panelRef.current.scrollHeight;
    }
  }, [runs]);

  if (runs.length === 0) return null;

  const toggleExpand = (id: string) => {
    setExpanded((prev) => ({ ...prev, [id]: !prev[id] }));
  };

  return (
    <div className="subagent-progress-panel" ref={panelRef}>
      <div className="subagent-header">
        <Wrench size={12} />
        <span>子 Agent 进度</span>
        <button
          className="subagent-clear-btn"
          onClick={() => setRuns([])}
          title="清除"
        >
          清除
        </button>
      </div>
      {runs.map((run) => (
        <div key={run.agentId} className="subagent-run">
          <div className="subagent-run-header" onClick={() => toggleExpand(run.agentId)}>
            {expanded[run.agentId] ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
            <span className="subagent-type">{run.agentType}</span>
            <span className="subagent-id">{run.agentId.slice(0, 12)}</span>
            {run.isDone ? (
              run.error ? <XCircle size={12} className="subagent-error-icon" /> : <CheckCircle2 size={12} className="subagent-done-icon" />
            ) : (
              <Loader2 size={12} className="spin subagent-running-icon" />
            )}
            <span className="subagent-step-count">{run.steps.length} 步</span>
          </div>
          {expanded[run.agentId] && (
            <div className="subagent-steps">
              {run.steps.map((step, i) => (
                <div key={i} className="subagent-step">
                  <div className="subagent-step-header">
                    <span className="subagent-iteration">#{step.iteration}</span>
                    {step.toolName && (
                      <span className="subagent-tool-name">
                        <Wrench size={10} /> {step.toolName}
                      </span>
                    )}
                    {step.isDone && !step.error && (
                      <CheckCircle2 size={10} className="subagent-done-icon" />
                    )}
                    {step.error && (
                      <XCircle size={10} className="subagent-error-icon" />
                    )}
                  </div>
                  {step.output && (
                    <div className="subagent-output">{step.output.slice(0, 300)}{step.output.length > 300 ? "…" : ""}</div>
                  )}
                  {step.error && (
                    <div className="subagent-error">{step.error}</div>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      ))}
    </div>
  );
}
