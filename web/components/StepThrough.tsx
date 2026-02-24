import { ChevronLeft, ChevronRight, Pause, Play } from "lucide-preact";
import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useRef,
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

interface StepState {
  loading: boolean;
  error: string | null;
  sessionId: string | null;
  steppable: boolean | null;
  done: boolean;
  phase: string;
  currentStep: number;
  history: StepRecord[];
  historyCursor: number;
  playing: boolean;
  singlePassMode: "before" | "after";
  singlePassResult: TraceResult | null;
}

type StepAction =
  | { type: "RESET" }
  | { type: "START_CHECK" }
  | {
    type: "INIT_SESSION";
    sessionId: string | null;
    steppable: boolean;
    done: boolean;
    currentStep: number;
  }
  | {
    type: "INIT_SINGLE_PASS";
    sessionId: string | null;
    done: boolean;
    currentStep: number;
    singlePassResult: TraceResult;
  }
  | { type: "SET_ERROR"; error: string }
  | { type: "STEP"; record: StepRecord }
  | { type: "STEP_DONE"; record: StepRecord }
  | { type: "NAVIGATE"; cursor: number }
  | { type: "STOP_PLAY" }
  | { type: "START_PLAY" }
  | { type: "SET_SINGLE_PASS_MODE"; mode: "before" | "after" };

const initialState: StepState = {
  loading: false,
  error: null,
  sessionId: null,
  steppable: null,
  done: false,
  phase: "-",
  currentStep: 0,
  history: [],
  historyCursor: 0,
  playing: false,
  singlePassMode: "after",
  singlePassResult: null,
};

function stepReducer(state: StepState, action: StepAction): StepState {
  switch (action.type) {
    case "RESET":
      return initialState;
    case "START_CHECK":
      return { ...state, error: null, playing: false, loading: true };
    case "INIT_SESSION":
      return {
        ...state,
        sessionId: action.sessionId,
        steppable: action.steppable,
        done: action.done,
        currentStep: action.currentStep,
        phase: "-",
        history: [],
        historyCursor: 0,
        loading: false,
      };
    case "INIT_SINGLE_PASS":
      return {
        ...state,
        sessionId: action.sessionId,
        steppable: false,
        done: action.done,
        currentStep: action.currentStep,
        phase: "-",
        history: [],
        historyCursor: 0,
        loading: false,
        singlePassResult: action.singlePassResult,
        singlePassMode: "after",
      };
    case "SET_ERROR":
      return { ...state, error: action.error, loading: false };
    case "STEP": {
      const newHistory = [...state.history, action.record];
      return {
        ...state,
        currentStep: action.record.step,
        phase: action.record.phase,
        history: newHistory,
        historyCursor: newHistory.length - 1,
      };
    }
    case "STEP_DONE": {
      const newHistory = [...state.history, action.record];
      return {
        ...state,
        currentStep: action.record.step,
        phase: action.record.phase,
        history: newHistory,
        historyCursor: newHistory.length - 1,
        done: true,
        playing: false,
      };
    }
    case "NAVIGATE": {
      const record = state.history[action.cursor];
      return {
        ...state,
        historyCursor: action.cursor,
        currentStep: record.step,
        phase: record.phase,
      };
    }
    case "STOP_PLAY":
      return { ...state, playing: false };
    case "START_PLAY":
      return { ...state, playing: true };
    case "SET_SINGLE_PASS_MODE":
      return { ...state, singlePassMode: action.mode };
  }
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
  const [state, dispatch] = useReducer(stepReducer, initialState);
  const intervalRef = useRef<number | null>(null);

  const stepLabel = useMemo(
    () => `Step ${state.currentStep}`,
    [state.currentStep],
  );

  const stopPlay = useCallback(() => {
    if (intervalRef.current != null) {
      globalThis.clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
    dispatch({ type: "STOP_PLAY" });
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
      dispatch({ type: "SET_SINGLE_PASS_MODE", mode });
      if (!state.singlePassResult) return;
      if (mode === "after") {
        onResult(state.singlePassResult);
      } else {
        onResult({ ...state.singlePassResult, witness_edges: [] });
      }
    },
    [state.singlePassResult, onResult],
  );

  const nextStep = useCallback(() => {
    if (!state.sessionId || state.done || state.steppable !== true) return;
    try {
      const response = JSON.parse(
        check_consistency_step_next(state.sessionId),
      ) as StepNextResponse;
      if (response.error) {
        dispatch({ type: "SET_ERROR", error: response.error });
        stopPlay();
        return;
      }

      const step = typeof response.step === "number"
        ? response.step
        : state.currentStep;
      const currentPhase = response.phase ?? state.phase;
      const edges = parseEdges(response.new_edges);
      const record: StepRecord = {
        step,
        phase: currentPhase ?? "-",
        newEdges: edges,
      };

      applyHighlight(edges);

      if (response.done) {
        dispatch({ type: "STEP_DONE", record });
        stopPlay();
        if (response.result) {
          onResult(response.result);
        }
      } else {
        dispatch({ type: "STEP", record });
      }
    } catch (err) {
      dispatch({
        type: "SET_ERROR",
        error: err instanceof Error ? err.message : String(err),
      });
      stopPlay();
    }
  }, [
    applyHighlight,
    state.currentStep,
    state.done,
    onResult,
    state.phase,
    state.sessionId,
    state.steppable,
    stopPlay,
  ]);

  const startStepCheck = useCallback(() => {
    dispatch({ type: "START_CHECK" });
    stopPlay();
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
        dispatch({ type: "SET_ERROR", error: response.error });
        return;
      }

      const initEdges = parseEdges(response.new_edges);
      applyHighlight(initEdges);

      if (response.steppable === false && response.result) {
        dispatch({
          type: "INIT_SINGLE_PASS",
          sessionId: response.session_id ?? null,
          done: Boolean(response.done),
          currentStep: response.step ?? 0,
          singlePassResult: response.result,
        });
        onResult(response.result);
      } else {
        dispatch({
          type: "INIT_SESSION",
          sessionId: response.session_id ?? null,
          steppable: response.steppable ?? false,
          done: Boolean(response.done),
          currentStep: response.step ?? 0,
        });
      }
    } catch (err) {
      dispatch({
        type: "SET_ERROR",
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }, [applyHighlight, format, input, level, onResult, stopPlay]);

  const prevStep = useCallback(() => {
    if (state.history.length === 0) return;
    const nextCursor = Math.max(0, state.historyCursor - 1);
    dispatch({ type: "NAVIGATE", cursor: nextCursor });
    applyHighlight(state.history[nextCursor].newEdges);
  }, [applyHighlight, state.history, state.historyCursor]);

  const nextFromHistoryOrApi = useCallback(() => {
    if (state.historyCursor < state.history.length - 1) {
      const nextCursor = state.historyCursor + 1;
      dispatch({ type: "NAVIGATE", cursor: nextCursor });
      applyHighlight(state.history[nextCursor].newEdges);
      return;
    }
    nextStep();
  }, [applyHighlight, state.history, state.historyCursor, nextStep]);

  useEffect(() => {
    if (!state.playing) return;
    intervalRef.current = globalThis.setInterval(() => {
      nextFromHistoryOrApi();
    }, 800);
    return () => {
      if (intervalRef.current != null) {
        globalThis.clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
    };
  }, [nextFromHistoryOrApi, state.playing]);

  useEffect(() => {
    dispatch({ type: "RESET" });
  }, [input, level, format]);

  useEffect(() => {
    if (state.done) stopPlay();
  }, [state.done, stopPlay]);

  return (
    <div class="step-through">
      <button
        type="button"
        class="btn check-btn"
        onClick={startStepCheck}
        disabled={state.loading}
      >
        <Play size={14} /> Step Check
      </button>

      {state.steppable === true && (
        <div class="step-controls">
          <button
            type="button"
            class="btn btn-sm"
            onClick={prevStep}
            disabled={state.history.length === 0 ||
              state.historyCursor === 0}
            aria-label="Previous step"
          >
            <ChevronLeft size={14} /> Prev
          </button>
          <span class="step-counter">{stepLabel}</span>
          <span class="step-phase">{state.phase}</span>
          <button
            type="button"
            class="btn btn-sm"
            onClick={nextFromHistoryOrApi}
            disabled={!state.sessionId || state.done}
            aria-label="Next step"
          >
            Next <ChevronRight size={14} />
          </button>
          <button
            type="button"
            class="btn btn-sm"
            onClick={() =>
              state.playing ? stopPlay() : dispatch({ type: "START_PLAY" })}
            disabled={!state.sessionId || state.done}
            aria-label={state.playing ? "Pause playback" : "Play steps"}
          >
            {state.playing ? <Pause size={14} /> : <Play size={14} />}
            {state.playing ? "Pause" : "Play"}
          </button>
        </div>
      )}

      {state.steppable === false && (
        <div class="step-controls">
          <span class="step-counter">Witness Edges</span>
          <div class="step-toggle-group">
            <button
              type="button"
              class={`btn btn-sm ${
                state.singlePassMode === "before" ? "btn-primary" : ""
              }`}
              onClick={() => applySinglePassMode("before")}
            >
              Hide
            </button>
            <button
              type="button"
              class={`btn btn-sm ${
                state.singlePassMode === "after" ? "btn-primary" : ""
              }`}
              onClick={() => applySinglePassMode("after")}
            >
              Show
            </button>
          </div>
        </div>
      )}

      {state.error && <div class="step-error">{state.error}</div>}
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
