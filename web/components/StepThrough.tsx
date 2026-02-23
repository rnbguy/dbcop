import { ChevronLeft, ChevronRight, Pause, Play } from "lucide-preact";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "preact/hooks";
import type {
  ConsistencyLevel,
  InputFormat,
  TraceResult,
  TransactionId,
} from "../types.ts";
import {
  check_consistency_step_init,
  check_consistency_step_init_text,
  check_consistency_step_next,
} from "../wasm.ts";

type EdgePair = [TransactionId, TransactionId];

interface StepInitResponse {
  error?: string;
  session_id?: string;
  step?: number;
  level?: string;
  steppable?: boolean;
  done?: boolean;
  result?: TraceResult;
  new_edges?: EdgePair[];
}

interface StepNextResponse {
  error?: string;
  session_id?: string;
  step?: number;
  phase?: "ww" | "rw" | "vis";
  new_edges?: EdgePair[];
  total_edges?: number;
  done?: boolean;
  result?: TraceResult;
}

interface StepRecord {
  step: number;
  phase: string;
  newEdges: EdgePair[];
}

interface Props {
  input: string;
  level: ConsistencyLevel;
  format: InputFormat;
  graphRef: {
    highlightEdges: (edges: EdgePair[]) => void;
  } | null;
  onResult: (result: TraceResult) => void;
}

export function StepThrough(
  { input, level, format, graphRef, onResult }: Props,
) {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [steppable, setSteppable] = useState<boolean | null>(null);
  const [done, setDone] = useState(false);
  const [phase, setPhase] = useState<string>("-");
  const [currentStep, setCurrentStep] = useState(0);
  const [history, setHistory] = useState<StepRecord[]>([]);
  const [historyCursor, setHistoryCursor] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [singlePassMode, setSinglePassMode] = useState<"before" | "after">(
    "after",
  );
  const [singlePassResult, setSinglePassResult] = useState<TraceResult | null>(
    null,
  );
  const intervalRef = useRef<number | null>(null);

  const resetState = useCallback(() => {
    setLoading(false);
    setError(null);
    setSessionId(null);
    setSteppable(null);
    setDone(false);
    setPhase("-");
    setCurrentStep(0);
    setHistory([]);
    setHistoryCursor(0);
    setPlaying(false);
    setSinglePassMode("after");
    setSinglePassResult(null);
  }, []);

  const stepLabel = useMemo(() => `Step ${currentStep}`, [currentStep]);

  const stopPlay = useCallback(() => {
    if (intervalRef.current != null) {
      globalThis.clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
    setPlaying(false);
  }, []);

  const parseEdges = (edges: unknown): EdgePair[] => {
    if (!Array.isArray(edges)) return [];
    const out: EdgePair[] = [];
    for (const pair of edges) {
      if (!Array.isArray(pair) || pair.length !== 2) continue;
      const [from, to] = pair;
      if (!isTransactionId(from) || !isTransactionId(to)) continue;
      out.push([from, to]);
    }
    return out;
  };

  const applyHighlight = useCallback(
    (edges: EdgePair[]) => {
      if (edges.length === 0) return;
      graphRef?.highlightEdges(edges);
    },
    [graphRef],
  );

  const applySinglePassMode = useCallback(
    (mode: "before" | "after") => {
      setSinglePassMode(mode);
      if (!singlePassResult) return;
      if (mode === "after") {
        onResult(singlePassResult);
      } else {
        onResult({ ...singlePassResult, witness_edges: [] });
      }
    },
    [singlePassResult, onResult],
  );

  const nextStep = useCallback(() => {
    if (!sessionId || done || steppable !== true) return;
    try {
      const response = JSON.parse(
        check_consistency_step_next(sessionId),
      ) as StepNextResponse;
      if (response.error) {
        setError(response.error);
        stopPlay();
        return;
      }

      const step = typeof response.step === "number"
        ? response.step
        : currentStep;
      const currentPhase = response.phase ?? phase;
      const edges = parseEdges(response.new_edges);

      setCurrentStep(step);
      setPhase(currentPhase ?? "-");
      applyHighlight(edges);

      setHistory((prev) => {
        const next = [...prev, {
          step,
          phase: currentPhase ?? "-",
          newEdges: edges,
        }];
        setHistoryCursor(next.length - 1);
        return next;
      });

      if (response.done) {
        setDone(true);
        stopPlay();
        if (response.result) {
          onResult(response.result);
        }
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      stopPlay();
    }
  }, [
    applyHighlight,
    currentStep,
    done,
    onResult,
    phase,
    sessionId,
    steppable,
    stopPlay,
  ]);

  const startStepCheck = useCallback(() => {
    setError(null);
    stopPlay();
    setLoading(true);
    try {
      // Parser requires trailing newline on every line
      const effectiveInput = format === "text" && !input.endsWith("\n")
        ? input + "\n"
        : input;
      const initFn = format === "text"
        ? check_consistency_step_init_text
        : check_consistency_step_init;
      const response = JSON.parse(
        initFn(effectiveInput, level),
      ) as StepInitResponse;
      if (response.error) {
        setError(response.error);
        return;
      }

      setSessionId(response.session_id ?? null);
      setSteppable(response.steppable ?? false);
      setDone(Boolean(response.done));
      setCurrentStep(response.step ?? 0);
      setPhase("-");
      setHistory([]);
      setHistoryCursor(0);
      const initEdges = parseEdges(response.new_edges);
      applyHighlight(initEdges);

      if (response.steppable === false && response.result) {
        setSinglePassResult(response.result);
        setSinglePassMode("after");
        onResult(response.result);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [applyHighlight, format, input, level, onResult, stopPlay]);

  const prevStep = useCallback(() => {
    if (history.length === 0) return;
    const nextCursor = Math.max(0, historyCursor - 1);
    setHistoryCursor(nextCursor);
    const record = history[nextCursor];
    setCurrentStep(record.step);
    setPhase(record.phase);
    applyHighlight(record.newEdges);
  }, [applyHighlight, history, historyCursor]);

  const nextFromHistoryOrApi = useCallback(() => {
    if (historyCursor < history.length - 1) {
      const nextCursor = historyCursor + 1;
      setHistoryCursor(nextCursor);
      const record = history[nextCursor];
      setCurrentStep(record.step);
      setPhase(record.phase);
      applyHighlight(record.newEdges);
      return;
    }
    nextStep();
  }, [applyHighlight, history, historyCursor, nextStep]);

  useEffect(() => {
    if (!playing) return;
    intervalRef.current = globalThis.setInterval(() => {
      nextFromHistoryOrApi();
    }, 800);
    return () => {
      if (intervalRef.current != null) {
        globalThis.clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
    };
  }, [nextFromHistoryOrApi, playing]);

  useEffect(() => {
    resetState();
  }, [input, level, format, resetState]);

  useEffect(() => {
    if (done) stopPlay();
  }, [done, stopPlay]);

  return (
    <div class="step-through">
      <button
        type="button"
        class="btn check-btn"
        onClick={startStepCheck}
        disabled={loading}
      >
        <Play size={14} /> Step Check
      </button>

      {steppable === true && (
        <div class="step-controls">
          <button
            type="button"
            class="btn btn-sm"
            onClick={prevStep}
            disabled={history.length === 0 || historyCursor === 0}
            aria-label="Previous step"
          >
            <ChevronLeft size={14} /> Prev
          </button>
          <span class="step-counter">{stepLabel}</span>
          <span class="step-phase">{phase}</span>
          <button
            type="button"
            class="btn btn-sm"
            onClick={nextFromHistoryOrApi}
            disabled={!sessionId || done}
            aria-label="Next step"
          >
            Next <ChevronRight size={14} />
          </button>
          <button
            type="button"
            class="btn btn-sm"
            onClick={() => (playing ? stopPlay() : setPlaying(true))}
            disabled={!sessionId || done}
            aria-label={playing ? "Pause playback" : "Play steps"}
          >
            {playing ? <Pause size={14} /> : <Play size={14} />}
            {playing ? "Pause" : "Play"}
          </button>
        </div>
      )}

      {steppable === false && (
        <div class="step-controls">
          <span class="step-counter">Single-pass level</span>
          <div class="step-toggle-group">
            <button
              type="button"
              class={`btn btn-sm ${
                singlePassMode === "before" ? "btn-primary" : ""
              }`}
              onClick={() => applySinglePassMode("before")}
            >
              Before
            </button>
            <button
              type="button"
              class={`btn btn-sm ${
                singlePassMode === "after" ? "btn-primary" : ""
              }`}
              onClick={() => applySinglePassMode("after")}
            >
              After
            </button>
          </div>
        </div>
      )}

      {error && <div class="step-error">{error}</div>}
    </div>
  );
}

function isTransactionId(value: unknown): value is TransactionId {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate.session_id === "number" &&
    typeof candidate.session_height === "number"
  );
}
