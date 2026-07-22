#!/usr/bin/env bash
set -euo pipefail

# snipe local runner — three-phase batch pipeline, fully local (Ollama only)
#
# Phase 1 (parallel Ollama): Scores every offer cheaply + fetches JD.
#                            Writes batch/scores/<id>.json + /tmp/batch-jd-<id>.txt
#
# Phase 2 (parallel Ollama): Full A-G evaluation for all scored offers.
#                            Node.js web-searcher runs first (no LLM).
#                            Writes reports/<NNN>-<slug>-<date>.md
#
# Phase 3 (selective Ollama): CV tailoring + PDF only for offers scoring >= threshold,
#                             on the local snipe-cv coder model.
#
# Offers below threshold: orchestrator writes tracker line directly (no LLM).
#
# State: batch/local-state.tsv (10-col format, auto-migrated from 9-col)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BATCH_DIR="$SCRIPT_DIR"
INPUT_FILE="$BATCH_DIR/batch-input.tsv"
STATE_FILE="$BATCH_DIR/local-state.tsv"
SCORE_PROMPT="$BATCH_DIR/ollama-score-prompt.md"
EVAL_PROMPT="$BATCH_DIR/ollama-eval-prompt.md"
SCORES_DIR="$BATCH_DIR/scores"
LOGS_DIR="$BATCH_DIR/logs"
TRACKER_DIR="$BATCH_DIR/tracker-additions"
REPORTS_DIR="$PROJECT_DIR/reports"
APPLICATIONS_FILE="$PROJECT_DIR/data/applications.md"
LOCK_FILE="$BATCH_DIR/local-runner.pid"
STATE_LOCK_DIR="$BATCH_DIR/.local-state.lock"
STATE_LOCK_PID_FILE="$STATE_LOCK_DIR/pid"
STATE_LOCK_TIMEOUT_SECONDS=30
MAIN_PID="${BASHPID:-$$}"

# Defaults
PARALLEL_SCORE=1
PARALLEL_EVAL=1
PARALLEL_PDF=2
THRESHOLD=""
P1_THRESHOLD="2.5"
LOCAL_CTX=8192
OLLAMA_MODEL="snipe-screen"
# Phase 2 model — benchmarked 2026-07-15 (batch/bench): qwen3 30B-A3B beats the
# 7B on calibration (full 1-5 range, correct junior/principal direction) at
# ~2x latency. Phase 1 runs qwen3-4b-2507 (snipe-screen): bulk pre-filter,
# benchmarked 2026-07-17 vs qwen2.5-7b (score collapse, fabricated hard stops)
# and qwen3-8b (better judgment but 20%+ CPU spill and ~5x slower).
PHASE2_MODEL="snipe-eval"
# Phase 3 (CV tailoring) uses a separate, coder-based model. Benchmarking showed
# qwen2.5-coder follows the structured JSON constraints (word count, project
# count, relevant selection) markedly better than the general-instruct eval
# model, with no prose-quality loss. Eval (Phase 1/2) stays on OLLAMA_MODEL so
# scoring is unchanged. One model swap at the Phase 2→3 boundary per batch.
PHASE3_MODEL="snipe-cv"
OLLAMA_URL="http://localhost:11434"
DRY_RUN=false
SKIP_PHASE1=false
SKIP_PHASE2=false
SKIP_PHASE3=false
RETRY_FAILED=false
START_FROM=0
MAX_RETRIES=2
ONLY_ID=""
# Phase 2 evaluator script. Staged (3-stage: JD parse → embedding evidence
# match → judgment, report assembled in code) is the measured default —
# 100% grounded evidence, continuous scores, junk-posting guard. --classic-eval
# reverts to the monolithic evaluator.
EVALUATOR_SCRIPT="staged-evaluator.mjs"

usage() {
  cat <<'USAGE'
snipe local runner — Ollama scoring + evaluation + CV tailoring (fully local)

Phase 1: Scores all pending offers with Ollama (parallel, cheap).
Phase 2: Full A-G evaluation via Ollama + Node.js web search (no LLM for search).
Phase 3: Local coder model tailors CV + PDF only for offers scoring >= threshold.

Usage: local-runner.sh [OPTIONS]

Options:
  --parallel-score N    Phase 1 Ollama scorers (default: 1)
  --parallel-eval N     Phase 2 Ollama evaluators (default: 1)
  --parallel-pdf N      Phase 3 PDF workers (default: 2; VRAM-shared — keep low)
  --parallel-tailor N   Alias for --parallel-pdf (backwards compat)
  --threshold N         Phase 2→3 PDF threshold (default: auto from profile.yml, else 3.0)
  --p1-threshold N      Phase 1→2 gate: skip Phase 2 for offers below this score (default: 2.5; pass 0 to disable)
  --local-ctx N         Context window for Phase 3 Ollama call (default: 8192)
  --ollama-model NAME   Ollama model for Phase 1 screening (default: snipe-screen)
  --phase3-model NAME   Ollama model for Phase 3 CV tailoring (default: snipe-cv, coder-based)
  --ollama-url URL      Ollama base URL (default: http://localhost:11434)
  --dry-run             Show what would be processed, don't execute
  --skip-phase1         Skip scoring; use existing batch/scores/<id>.json
  --skip-phase2         Skip evaluation; use existing reports (implies --skip-phase1)
  --skip-phase3         Score + evaluate only — skip CV tailoring/PDFs
  --staged-eval         Use staged-evaluator.mjs for Phase 2 (DEFAULT; needs
                        snipe-embed built)
  --classic-eval        Use the monolithic ollama-evaluator.mjs for Phase 2
  --phase2-model NAME   Ollama model for Phase 2 (default: snipe-eval)
  --retry-failed        Retry offers marked as failed in any phase
  --start-from N        Skip offers with ID < N
  --only-id N           Process a single offer by ID (testing)
  --max-retries N       Max retry attempts per offer (default: 2)
  -h, --help            Show this help

Files:
  batch-input.tsv           Input offers (id, url, source, notes)
  local-state.tsv           Processing state (auto-managed, 10-column)
  batch/scores/<id>.json    Phase 1 results
  reports/<NNN>-slug.md     Phase 2 evaluation reports
  output/cv-*.pdf           Phase 3 tailored PDFs
  logs/score-<id>.log       Phase 1 per-offer logs
  logs/eval-NNN-<id>.log    Phase 2 per-offer logs
  logs/pdf-NNN-<id>.log     Phase 3 per-offer logs

Examples:
  # Full pipeline
  ./local-runner.sh

  # Score + evaluate only (skip PDFs)
  ./local-runner.sh --skip-phase3

  # Score only
  ./local-runner.sh --skip-phase2 --skip-phase3

  # Re-generate PDFs after changing the template
  ./local-runner.sh --skip-phase1 --skip-phase2

  # Retry all failures
  ./local-runner.sh --retry-failed
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --parallel-score)   PARALLEL_SCORE="$2"; shift 2 ;;
    --parallel-eval)    PARALLEL_EVAL="$2"; shift 2 ;;
    --parallel-pdf)     PARALLEL_PDF="$2"; shift 2 ;;
    --parallel-tailor)  PARALLEL_PDF="$2"; shift 2 ;;  # backwards compat
    --threshold)        THRESHOLD="$2"; shift 2 ;;
    --p1-threshold)     P1_THRESHOLD="$2"; shift 2 ;;
    --local-ctx)        LOCAL_CTX="$2"; shift 2 ;;
    --ollama-model)     OLLAMA_MODEL="$2"; shift 2 ;;
    --phase3-model)     PHASE3_MODEL="$2"; shift 2 ;;
    --ollama-url)       OLLAMA_URL="$2"; shift 2 ;;
    --dry-run)          DRY_RUN=true; shift ;;
    --skip-phase1)      SKIP_PHASE1=true; shift ;;
    --skip-phase2)      SKIP_PHASE2=true; shift ;;
    --skip-phase3)      SKIP_PHASE3=true; shift ;;
    --staged-eval)      EVALUATOR_SCRIPT="staged-evaluator.mjs"; shift ;;
    --classic-eval)     EVALUATOR_SCRIPT="ollama-evaluator.mjs"; shift ;;
    --phase2-model)     PHASE2_MODEL="$2"; shift 2 ;;
    --retry-failed)     RETRY_FAILED=true; shift ;;
    --start-from)       START_FROM="$2"; shift 2 ;;
    --only-id)          ONLY_ID="$2"; shift 2 ;;
    --max-retries)      MAX_RETRIES="$2"; shift 2 ;;
    -h|--help)          usage; exit 0 ;;
    *)                  echo "Unknown option: $1"; usage; exit 1 ;;
  esac
done

# ── Prerequisites ─────────────────────────────────────────────────────────────

check_prerequisites() {
  if [[ ! -f "$INPUT_FILE" ]]; then
    echo "ERROR: $INPUT_FILE not found."
    exit 1
  fi
  for f in "$EVAL_PROMPT" "$BATCH_DIR/local-tailor-prompt.md"; do
    if [[ ! -f "$f" ]]; then
      echo "ERROR: $f not found."
      exit 1
    fi
  done
  local required_cmds=(node jq)
  for cmd in "${required_cmds[@]}"; do
    if ! command -v "$cmd" &>/dev/null; then
      echo "ERROR: '$cmd' not found in PATH."
      exit 1
    fi
  done

  mkdir -p "$SCORES_DIR" "$LOGS_DIR" "$TRACKER_DIR" "$REPORTS_DIR" "$PROJECT_DIR/output"

  if [[ -z "$THRESHOLD" ]]; then
    local yml_threshold
    yml_threshold=$(grep 'auto_pdf_score_threshold' "$PROJECT_DIR/config/profile.yml" 2>/dev/null \
      | awk -F': ' '{print $2}' | tr -d ' "' | head -1 || true)
    THRESHOLD="${yml_threshold:-3.0}"
  fi
  echo "PDF threshold: $THRESHOLD"
}

check_ollama() {
  if [[ "$SKIP_PHASE1" == "true" && "$SKIP_PHASE2" == "true" ]]; then return; fi

  if ! curl -sf "${OLLAMA_URL}/api/tags" >/dev/null 2>&1; then
    echo "ERROR: Ollama not running at $OLLAMA_URL. Start with: ollama serve"
    exit 1
  fi
  # Capture once: `ollama list | grep -q` under pipefail dies to a SIGPIPE race
  # when grep matches an early line and closes the pipe.
  local installed_models
  installed_models=$(ollama list 2>/dev/null || true)
  if ! grep -q "$(echo "$OLLAMA_MODEL" | cut -d: -f1)" <<< "$installed_models"; then
    echo "ERROR: Model '$OLLAMA_MODEL' not in Ollama. Pull with: ollama pull $OLLAMA_MODEL"
    exit 1
  fi
  # Phase 2 prerequisites: evaluation model + (staged only) embedding model.
  if [[ "$SKIP_PHASE2" == "false" ]]; then
    if ! grep -q "$(echo "$PHASE2_MODEL" | cut -d: -f1)" <<< "$installed_models"; then
      echo "ERROR: Phase-2 model '$PHASE2_MODEL' not in Ollama. Build it with:"
      echo "  ollama create snipe-eval -f batch/Modelfile.snipe-eval"
      echo "  (or use --phase2-model snipe-screen to fall back to the 4B)"
      exit 1
    fi
    if [[ "$EVALUATOR_SCRIPT" == "staged-evaluator.mjs" ]] \
        && ! grep -q 'snipe-embed' <<< "$installed_models"; then
      echo "ERROR: staged evaluator needs the embedding model. Build with:"
      echo "  ollama create snipe-embed -f batch/Modelfile.snipe-embed   (or run with --classic-eval)"
      exit 1
    fi
  fi
  if [[ "$SKIP_PHASE3" == "false" ]]; then
    if ! grep -q "$(echo "$PHASE3_MODEL" | cut -d: -f1)" <<< "$installed_models"; then
      echo "ERROR: Phase-3 model '$PHASE3_MODEL' not in Ollama. Build it with:"
      echo "  ollama create $PHASE3_MODEL -f batch/Modelfile.snipe-cv"
      exit 1
    fi
  fi
  echo "Ollama: $OLLAMA_MODEL @ $OLLAMA_URL"
}

# ── Lock ──────────────────────────────────────────────────────────────────────

acquire_lock() {
  if [[ -f "$LOCK_FILE" ]]; then
    local old_pid
    old_pid=$(cat "$LOCK_FILE")
    if kill -0 "$old_pid" 2>/dev/null; then
      echo "ERROR: Another local-runner is already running (PID $old_pid)"
      exit 1
    fi
    rm -f "$LOCK_FILE"
  fi
  echo "$MAIN_PID" > "$LOCK_FILE"
}

release_lock() {
  [[ "${BASHPID:-$$}" != "$MAIN_PID" ]] && return
  rm -f "$LOCK_FILE"
}

trap release_lock EXIT

acquire_state_lock() {
  local waited=0
  local max_waits=$((STATE_LOCK_TIMEOUT_SECONDS * 10))
  while true; do
    if mkdir "$STATE_LOCK_DIR" 2>/dev/null; then
      printf '%s\n' "${BASHPID:-$$}" > "$STATE_LOCK_PID_FILE"
      return 0
    fi
    if [[ -f "$STATE_LOCK_PID_FILE" ]]; then
      local lock_pid
      lock_pid=$(cat "$STATE_LOCK_PID_FILE" 2>/dev/null || true)
      if [[ -n "$lock_pid" ]] && ! kill -0 "$lock_pid" 2>/dev/null; then
        rm -f "$STATE_LOCK_PID_FILE"
        rmdir "$STATE_LOCK_DIR" 2>/dev/null && continue
      fi
    fi
    (( waited >= max_waits )) && { echo "ERROR: state lock timeout"; return 1; }
    sleep 0.1
    ((waited += 1))
  done
}

release_state_lock() {
  rm -f "$STATE_LOCK_PID_FILE" 2>/dev/null || true
  rmdir "$STATE_LOCK_DIR" 2>/dev/null || true
}

run_with_state_lock() {
  acquire_state_lock || return $?
  local status=0
  "$@" || status=$?
  release_state_lock
  return "$status"
}

# ── State (10-column format) ──────────────────────────────────────────────────
# Columns: id url p1_status p1_score p1_archetype p2_status p2_report_num p3_status error retries

STATE_HEADER="id	url	p1_status	p1_score	p1_archetype	p2_status	p2_report_num	p3_status	error	retries"

init_state() {
  if [[ ! -f "$STATE_FILE" ]]; then
    printf '%s\n' "$STATE_HEADER" > "$STATE_FILE"
    return
  fi

  # Migrate 9-column → 10-column: insert p3_status="-" after p2_report_num (col 7)
  local col_count
  col_count=$(head -1 "$STATE_FILE" | awk -F'\t' '{print NF}')
  if [[ "$col_count" -eq 9 ]]; then
    echo "Migrating local-state.tsv from 9-column to 10-column format..."
    local tmp="$STATE_FILE.migrate"
    printf '%s\n' "$STATE_HEADER" > "$tmp"
    tail -n +2 "$STATE_FILE" | while IFS=$'\t' read -r id url p1s p1sc p1a p2s rnum err ret; do
      local p3s="-"
      # Infer p3 status from old p2 status
      [[ "$p2s" == "completed" ]] && p3s="completed"
      [[ "$p2s" == "skipped" ]] && p3s="skipped"
      [[ "$p2s" == "tailor_failed" ]] && p3s="pdf_failed"
      # Rewrite p2 status
      [[ "$p2s" == "completed" || "$p2s" == "tailor_failed" ]] && p2s="evaled"
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$id" "$url" "$p1s" "$p1sc" "$p1a" "$p2s" "$rnum" "$p3s" "$err" "$ret"
    done >> "$tmp"
    mv "$tmp" "$STATE_FILE"
    echo "Migration complete."
  fi
}

get_field() {
  local id="$1" field="$2"
  local col
  case "$field" in
    p1_status)    col=3 ;;
    p1_score)     col=4 ;;
    p1_archetype) col=5 ;;
    p2_status)    col=6 ;;
    p2_report_num) col=7 ;;
    p3_status)    col=8 ;;
    retries)      col=10 ;;
    *)            echo ""; return ;;
  esac
  awk -F'\t' -v id="$id" -v col="$col" '$1 == id { print $col }' "$STATE_FILE" 2>/dev/null || true
}

update_state_unlocked() {
  # Args: id url p1_status p1_score p1_archetype p2_status p2_report_num p3_status error retries
  local id="$1" url="$2" p1s="$3" p1sc="$4" p1a="$5" p2s="$6" rnum="$7" p3s="$8" err="$9" ret="${10}"

  [[ ! -f "$STATE_FILE" ]] && init_state

  local tmp="$STATE_FILE.tmp"
  local found=false
  head -1 "$STATE_FILE" > "$tmp"

  while IFS=$'\t' read -r sid surl sp1s sp1sc sp1a sp2s srnum sp3s serr sret; do
    [[ "$sid" == "id" ]] && continue
    if [[ "$sid" == "$id" ]]; then
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$id" "$url" "$p1s" "$p1sc" "$p1a" "$p2s" "$rnum" "$p3s" "$err" "$ret" >> "$tmp"
      found=true
    else
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$sid" "$surl" "$sp1s" "$sp1sc" "$sp1a" "$sp2s" "$srnum" "$sp3s" "$serr" "$sret" >> "$tmp"
    fi
  done < "$STATE_FILE"

  [[ "$found" == "false" ]] && printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$id" "$url" "$p1s" "$p1sc" "$p1a" "$p2s" "$rnum" "$p3s" "$err" "$ret" >> "$tmp"

  mv "$tmp" "$STATE_FILE"
}

update_state() { run_with_state_lock update_state_unlocked "$@"; }

# Back-fill state from existing score/eval files so interrupted runs don't re-process.
reconcile_state() {
  local reconciled=0
  while IFS=$'\t' read -r id url _rest; do
    [[ "$id" == "id" || -z "$id" || -z "$url" ]] && continue
    [[ "$id" =~ ^[0-9]+$ ]] || continue

    local p1_status; p1_status=$(get_field "$id" "p1_status")

    # ── Phase 1: back-fill from batch/scores/<id>.json ──────────────────────
    if [[ "$p1_status" != "scored" && "$p1_status" != "unavailable" ]]; then
      local score_file="$SCORES_DIR/${id}.json"
      if [[ -s "$score_file" ]]; then
        local file_status; file_status=$(jq -r '.status // ""' "$score_file" 2>/dev/null || echo "")
        if [[ "$file_status" == "unavailable" ]]; then
          local file_err; file_err=$(jq -r '.error // "unavailable"' "$score_file" 2>/dev/null || echo "unavailable")
          update_state "$id" "$url" "unavailable" "-" "-" "-" "-" "-" "$file_err" "0"
        else
          local score archetype
          score=$(jq -r '.score // "-"' "$score_file" 2>/dev/null || echo "-")
          archetype=$(jq -r '.archetype // "-"' "$score_file" 2>/dev/null || echo "-")
          update_state "$id" "$url" "scored" "$score" "$archetype" "-" "-" "-" "-" "0"
        fi
        (( reconciled++ )) || true
      fi
    fi

    # ── Phase 2: back-fill from batch/evals/<id>.json ──────────────────────
    local p2_status; p2_status=$(get_field "$id" "p2_status")
    if [[ "$p2_status" != "evaled" ]]; then
      local eval_file; eval_file=$(find_eval_file "$id")
      if [[ -n "$eval_file" ]]; then
        local p1_score p1_arch p2_rnum
        p1_score=$(get_field "$id" "p1_score")
        p1_arch=$(get_field "$id" "p1_archetype")
        p2_rnum=$(jq -r '.report_num // "-"' "$eval_file" 2>/dev/null || echo "-")
        update_state "$id" "$url" "scored" "$p1_score" "$p1_arch" "evaled" "$p2_rnum" "-" "-" "0"
        (( reconciled++ )) || true
      fi
    fi
  done < "$INPUT_FILE"

  [[ $reconciled -gt 0 ]] && echo "  Reconciled $reconciled entries from existing score/eval files." || true
}

next_report_num_unlocked() {
  local max_num=0
  for f in "$REPORTS_DIR"/*.md; do
    [[ -f "$f" ]] || continue
    local num="${f##*/}"; num="${num%%-*}"
    num=$((10#${num:-0})) 2>/dev/null || continue
    (( num > max_num )) && max_num=$num
  done
  if [[ -f "$STATE_FILE" ]]; then
    while IFS=$'\t' read -r _ _ _ _ _ _ rnum _ _ _; do
      [[ "$rnum" == "p2_report_num" || "$rnum" == "report_num" || "$rnum" == "-" || -z "$rnum" ]] && continue
      local n=$((10#$rnum)) 2>/dev/null || continue
      (( n > max_num )) && max_num=$n
    done < "$STATE_FILE"
  fi
  printf '%03d' $((max_num + 1))
}

# ── Phase 1: Ollama scoring ───────────────────────────────────────────────────

score_offer() {
  local id="$1" url="$2"
  local score_file="$SCORES_DIR/${id}.json"
  local log_file="$LOGS_DIR/score-${id}.log"

  echo "  [score] #$id: $url"

  # Use pre-fetched JD if available (e.g. from Apify for LinkedIn/Indeed/Glassdoor).
  local extra_args=()
  if [[ -s "$BATCH_DIR/jds/${id}.txt" ]]; then
    extra_args+=(--jd-file "$BATCH_DIR/jds/${id}.txt")
  fi

  local exit_code=0
  node "$BATCH_DIR/ollama-scorer.mjs" \
    --id "$id" --url "$url" \
    "${extra_args[@]}" \
    --model "$OLLAMA_MODEL" --ollama-url "$OLLAMA_URL" \
    > "$score_file" 2>"$log_file" || exit_code=$?

  if [[ $exit_code -ne 0 ]]; then
    local err
    # fatal() in scorer writes JSON to stdout (captured to score_file), not stderr
    err=$(jq -r '.error // ""' "$score_file" 2>/dev/null || true)
    if [[ -z "$err" ]]; then
      err=$(tail -3 "$log_file" 2>/dev/null | tr '\n' ' ' | cut -c1-200 || echo "exit $exit_code")
    fi
    local cur_retries; cur_retries=$(get_field "$id" "retries"); cur_retries=${cur_retries:-0}
    update_state "$id" "$url" "score_failed" "-" "-" "-" "-" "-" "$err" "$(( cur_retries + 1 ))"
    echo "    ✗ Score failed: $err"
    return 1
  fi

  # Posting expired/blocked — not a retryable failure
  if jq -e '.status == "unavailable"' "$score_file" >/dev/null 2>&1; then
    local err; err=$(jq -r '.error' "$score_file")
    update_state "$id" "$url" "unavailable" "-" "-" "-" "-" "-" "$err" "0"
    echo "    ⊘ Unavailable: $err"
    return 0
  fi

  if ! jq -e '.score' "$score_file" >/dev/null 2>&1; then
    local raw; raw=$(head -c 300 "$score_file")
    update_state "$id" "$url" "score_failed" "-" "-" "-" "-" "-" "invalid JSON: $raw" "0"
    echo "    ✗ Invalid score JSON"
    return 1
  fi

  local score archetype hard_stops
  score=$(jq -r '.score' "$score_file")
  archetype=$(jq -r '.archetype' "$score_file")
  hard_stops=$(jq -r '.hard_stops | join(", ")' "$score_file")

  update_state "$id" "$url" "scored" "$score" "$archetype" "-" "-" "-" "-" "0"

  local flag=""
  [[ -n "$hard_stops" ]] && flag=" ⚠ hard stops: $hard_stops"
  echo "    ✓ Score: $score/5 — $archetype$flag"
}

run_phase1() {
  local -a ids=("$@")
  echo ""
  echo "=== Phase 1: Ollama Scoring (${#ids[@]} offers, $PARALLEL_SCORE parallel) ==="
  echo ""

  local running=0
  local -a pids=()

  for id_url in "${ids[@]}"; do
    local id="${id_url%%|*}" url="${id_url##*|}"

    while (( ${#pids[@]} >= PARALLEL_SCORE )); do
      local -a alive=()
      for pid in "${pids[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then alive+=("$pid")
        else wait "$pid" 2>/dev/null || true; fi
      done
      pids=("${alive[@]+"${alive[@]}"}")
      (( ${#pids[@]} < PARALLEL_SCORE )) || sleep 0.5
    done

    score_offer "$id" "$url" &
    pids+=($!)
  done

  for pid in "${pids[@]+"${pids[@]}"}"; do wait "$pid" 2>/dev/null || true; done
}

# Returns the best available eval file path for an offer id (persistent > tmp)
find_eval_file() {
  local id="$1"
  local persistent="$BATCH_DIR/evals/${id}.json"
  local tmp="/tmp/batch-eval-${id}.json"
  if [[ -f "$persistent" ]]; then echo "$persistent"
  elif [[ -f "$tmp" ]]; then echo "$tmp"
  else echo ""
  fi
}

# ── Phase 2: Ollama evaluation ────────────────────────────────────────────────

eval_offer() {
  local id="$1" url="$2" p1_score="$3" p1_archetype="$4"
  local score_file="$SCORES_DIR/${id}.json"

  # Assign report number atomically
  local report_num
  report_num=$(run_with_state_lock next_report_num_unlocked)
  update_state "$id" "$url" "scored" "$p1_score" "$p1_archetype" "evaluating" "$report_num" "-" "-" "0"

  local log_file="$LOGS_DIR/eval-${report_num}-${id}.log"
  local tmp_eval_file="/tmp/batch-eval-${id}.json"
  local persistent_eval_file="$BATCH_DIR/evals/${id}.json"
  mkdir -p "$BATCH_DIR/evals"

  echo "  [eval] #$id report $report_num: $url (pre-score: $p1_score)"

  local exit_code=0
  node "$BATCH_DIR/$EVALUATOR_SCRIPT" \
    --id "$id" \
    --url "$url" \
    --report-num "$report_num" \
    --model "$PHASE2_MODEL" \
    --ollama-url "$OLLAMA_URL" \
    --threshold "$THRESHOLD" \
    > "$tmp_eval_file" 2>"$log_file" || exit_code=$?

  # Sync persistent copy if tmp was just written (evaluator writes its own persistent copy,
  # but in case it failed partway, the tmp may have the error JSON)
  [[ -f "$tmp_eval_file" && ! -f "$persistent_eval_file" ]] && cp "$tmp_eval_file" "$persistent_eval_file" 2>/dev/null || true

  local eval_file="$tmp_eval_file"

  if [[ $exit_code -ne 0 ]]; then
    # fatal() in the evaluator writes JSON to stdout (captured to eval_file), not stderr
    local err
    err=$(jq -r '.error // ""' "$eval_file" 2>/dev/null || true)
    if [[ -z "$err" ]]; then
      err=$(tail -3 "$log_file" 2>/dev/null | tr '\n' ' ' | cut -c1-200 || echo "exit $exit_code")
    fi
    update_state "$id" "$url" "scored" "$p1_score" "$p1_archetype" "eval_failed" "$report_num" "-" "$err" "0"
    echo "    ✗ Eval failed: $err"
    return 1
  fi

  if ! jq -e '.status' "$eval_file" >/dev/null 2>&1; then
    update_state "$id" "$url" "scored" "$p1_score" "$p1_archetype" "eval_failed" "$report_num" "-" "invalid eval JSON" "0"
    echo "    ✗ Invalid eval JSON"
    return 1
  fi

  local eval_status eval_score eval_company eval_role
  eval_status=$(jq -r '.status' "$eval_file")
  eval_score=$(jq -r '.score // "N/A"' "$eval_file")
  eval_company=$(jq -r '.company // "unknown"' "$eval_file")
  eval_role=$(jq -r '.role // "unknown"' "$eval_file")

  if [[ "$eval_status" != "evaled" ]]; then
    local err
    err=$(jq -r '.error // "unknown error"' "$eval_file")
    update_state "$id" "$url" "scored" "$p1_score" "$p1_archetype" "eval_failed" "$report_num" "-" "$err" "0"
    echo "    ✗ Eval returned error: $err"
    return 1
  fi

  update_state "$id" "$url" "scored" "$p1_score" "$p1_archetype" "evaled" "$report_num" "-" "-" "0"
  echo "    ✓ Evaluated (score: $eval_score/5, report: $report_num — $eval_company)"
}

run_phase2() {
  local -a entries=("$@")
  echo ""
  echo "=== Phase 2: Ollama Evaluation (${#entries[@]} offers, $PARALLEL_EVAL parallel) ==="
  echo ""

  local running=0
  local -a pids=()

  for entry in "${entries[@]}"; do
    local id="${entry%%|*}"; local tail="${entry#*|}"
    local url="${tail%%|*}"; tail="${tail#*|}"
    local score="${tail%%|*}"; local archetype="${tail#*|}"

    while (( ${#pids[@]} >= PARALLEL_EVAL )); do
      local -a alive=()
      for pid in "${pids[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then alive+=("$pid")
        else wait "$pid" 2>/dev/null || true; fi
      done
      pids=("${alive[@]+"${alive[@]}"}")
      (( ${#pids[@]} < PARALLEL_EVAL )) || sleep 1
    done

    eval_offer "$id" "$url" "$score" "$archetype" &
    pids+=($!)
  done

  for pid in "${pids[@]+"${pids[@]}"}"; do wait "$pid" 2>/dev/null || true; done
}

write_tracker_skip() {
  local id="$1" url="$2" p1_score="$3" report_num="$4"
  local eval_file; eval_file=$(find_eval_file "$id")
  local today; today=$(date +%Y-%m-%d)

  local company role score archetype report_path report_slug
  company=$(jq -r '.company // "unknown"' "$eval_file" 2>/dev/null || echo "unknown")
  role=$(jq -r '.role // "unknown"' "$eval_file" 2>/dev/null || echo "unknown")
  score=$(jq -r '.score // "'"$p1_score"'"' "$eval_file" 2>/dev/null || echo "$p1_score")
  archetype=$(jq -r '.archetype // "Unknown"' "$eval_file" 2>/dev/null || echo "Unknown")
  report_path=$(find "$REPORTS_DIR" -name "${report_num}-*.md" 2>/dev/null | head -1 || echo "")
  report_slug=$(basename "$report_path" .md 2>/dev/null || echo "${report_num}-unknown-${today}")

  local next_num=1
  if [[ -f "$APPLICATIONS_FILE" ]]; then
    next_num=$(awk -F'|' 'NR>2 && /^\|[[:space:]]*[0-9]/ {n=$2+0} END{print n+1}' "$APPLICATIONS_FILE" 2>/dev/null || echo 1)
  fi

  local tracker_file="$TRACKER_DIR/${id}.tsv"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$next_num" "$today" "$company" "$role" "Evaluated" "${score}/5" "❌" \
    "[${report_num}](reports/${report_slug}.md)" \
    "Below threshold ($THRESHOLD) — no PDF" \
    > "$tracker_file"

  echo "    ✓ Tracker written (below threshold, no PDF)"
}

# Tracker line for offers the Phase 1→2 gate (--p1-threshold) skipped. No Phase 2
# eval exists yet, so company/role come straight from the Phase 1 score file.
write_tracker_p1_skip() {
  local id="$1" url="$2" p1_score="$3"
  local score_file="$SCORES_DIR/${id}.json"
  local today; today=$(date +%Y-%m-%d)

  local company role
  company=$(jq -r '.company // "unknown"' "$score_file" 2>/dev/null || echo "unknown")
  role=$(jq -r '.role // "unknown"' "$score_file" 2>/dev/null || echo "unknown")

  local next_num=1
  if [[ -f "$APPLICATIONS_FILE" ]]; then
    next_num=$(awk -F'|' 'NR>2 && /^\|[[:space:]]*[0-9]/ {n=$2+0} END{print n+1}' "$APPLICATIONS_FILE" 2>/dev/null || echo 1)
  fi

  local tracker_file="$TRACKER_DIR/${id}.tsv"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$next_num" "$today" "$company" "$role" "Evaluated" "${p1_score}/5" "❌" \
    "no report — P1-gated" \
    "P1-gated (score $p1_score < $P1_THRESHOLD) — Phase 2 skipped, re-run with a lower/no --p1-threshold to re-score" \
    > "$tracker_file"

  echo "    ✓ Tracker written (P1-gated, Phase 2 skipped)"
}

# ── Phase 3: local Ollama PDF ────────────────────────────────────────────────

pdf_offer_local() {
  local id="$1" url="$2" p1_score="$3" p1_archetype="$4" report_num="$5"
  local eval_file; eval_file=$(find_eval_file "$id")
  local log_file="$LOGS_DIR/pdf-${report_num}-${id}.log"
  local today; today=$(date +%Y-%m-%d)
  local jd_file="/tmp/batch-jd-${id}.txt"

  local eval_score eval_company eval_role eval_report_path
  eval_score=$(jq -r '.score // "N/A"' "$eval_file" 2>/dev/null || echo "N/A")
  eval_company=$(jq -r '.company // "unknown"' "$eval_file" 2>/dev/null || echo "unknown")
  eval_role=$(jq -r '.role // "unknown"' "$eval_file" 2>/dev/null || echo "unknown")
  eval_report_path=$(jq -r '.report_path // ""' "$eval_file" 2>/dev/null || echo "")

  # Fallback: if eval JSON has a seniority keyword as company (Phase 2 mis-extraction),
  # parse the real company from the report title "# Evaluation: Company — Role"
  if echo "$eval_company" | grep -qiE "^(senior|staff|junior|lead|principal|mid|entry|associate)$"; then
    local report_company
    report_company=$(find "$REPORTS_DIR" -name "${report_num}-*.md" 2>/dev/null | head -1 |
      xargs -I{} grep -m1 "^# Evaluation:" {} 2>/dev/null |
      sed 's/# Evaluation: //;s/ —.*//' | xargs 2>/dev/null || true)
    [[ -n "$report_company" ]] && eval_company="$report_company"
  fi

  if [[ -z "$eval_report_path" || ! -f "$eval_report_path" ]]; then
    eval_report_path=$(find "$REPORTS_DIR" -name "${report_num}-*.md" 2>/dev/null | head -1 || echo "")
  fi

  if [[ -z "$eval_report_path" || ! -f "$eval_report_path" ]]; then
    update_state "$id" "$url" "scored" "$p1_score" "$p1_archetype" "evaled" "$report_num" "pdf_failed" "report not found" "0"
    echo "    ✗ Local PDF failed: report .md not found for $report_num"
    return 1
  fi

  echo "  [local-pdf] #$id report $report_num: $eval_company — $eval_role"

  local exit_code=0
  node "$SCRIPT_DIR/local-pdf-offer.mjs" \
    --id          "$id" \
    --url         "$url" \
    --report-path "$eval_report_path" \
    --report-num  "$report_num" \
    --jd-file     "$jd_file" \
    --eval-score  "$eval_score" \
    --company     "$eval_company" \
    --role        "$eval_role" \
    --date        "$today" \
    --p1-score    "$p1_score" \
    --p1-archetype "$p1_archetype" \
    --model       "$PHASE3_MODEL" \
    --ollama-url  "$OLLAMA_URL" \
    --threshold   "$THRESHOLD" \
    --num-ctx     "$LOCAL_CTX" \
    > "$log_file" 2>&1 || exit_code=$?

  if [[ $exit_code -eq 0 ]]; then
    local pdf_path
    pdf_path=$(grep -oP '"pdf":\s*"\K[^"]+' "$log_file" 2>/dev/null | head -1 || true)
    update_state "$id" "$url" "scored" "$p1_score" "$p1_archetype" "evaled" "$report_num" "completed" "-" "0"
    echo "    ✓ Local PDF generated (report: $report_num${pdf_path:+ → $pdf_path})"
  else
    local err
    err=$(tail -5 "$log_file" 2>/dev/null | tr '\n' ' ' | cut -c1-200 || echo "exit $exit_code")
    update_state "$id" "$url" "scored" "$p1_score" "$p1_archetype" "evaled" "$report_num" "pdf_failed" "$err" "0"
    echo "    ✗ Local PDF failed (exit $exit_code)"
  fi
}

run_phase3() {
  local -a entries=("$@")
  echo ""
  echo "=== Phase 3: Local Ollama PDF (${#entries[@]} offers above threshold $THRESHOLD, $PARALLEL_PDF parallel) ==="
  echo ""

  local running=0
  local -a pids=()

  for entry in "${entries[@]}"; do
    local id="${entry%%|*}"; local tail="${entry#*|}"
    local url="${tail%%|*}"; tail="${tail#*|}"
    local score="${tail%%|*}"; tail="${tail#*|}"
    local archetype="${tail%%|*}"; local report_num="${tail#*|}"

    while (( ${#pids[@]} >= PARALLEL_PDF )); do
      local -a alive=()
      for pid in "${pids[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then alive+=("$pid")
        else wait "$pid" 2>/dev/null || true; fi
      done
      pids=("${alive[@]+"${alive[@]}"}")
      (( ${#pids[@]} < PARALLEL_PDF )) || sleep 1
    done

    pdf_offer_local "$id" "$url" "$score" "$archetype" "$report_num" &
    pids+=($!)
  done

  for pid in "${pids[@]+"${pids[@]}"}"; do wait "$pid" 2>/dev/null || true; done
}

# ── Summary ───────────────────────────────────────────────────────────────────

print_summary() {
  echo ""
  echo "=== Local Batch Summary ==="
  [[ ! -f "$STATE_FILE" ]] && echo "No state file." && return

  local total=0 scored=0 score_failed=0 unavailable=0 evaled=0 eval_failed=0 pdfs=0 pdf_failed=0 skipped=0
  local score_sum=0 score_count=0

  while IFS=$'\t' read -r sid _ sp1s sp1sc _ sp2s _ sp3s _ _; do
    [[ "$sid" == "id" ]] && continue
    total=$((total + 1))
    case "$sp1s" in
      scored)       scored=$((scored + 1))
                    if [[ "$sp1sc" != "-" && -n "$sp1sc" ]]; then
                      score_sum=$(echo "$score_sum + $sp1sc" | bc 2>/dev/null || echo "$score_sum")
                      score_count=$((score_count + 1))
                    fi ;;
      score_failed) score_failed=$((score_failed + 1)) ;;
      unavailable)  unavailable=$((unavailable + 1)) ;;
    esac
    case "$sp2s" in
      evaled)      evaled=$((evaled + 1)) ;;
      eval_failed) eval_failed=$((eval_failed + 1)) ;;
    esac
    case "$sp3s" in
      completed)   pdfs=$((pdfs + 1)) ;;
      pdf_failed)  pdf_failed=$((pdf_failed + 1)) ;;
      skipped)     skipped=$((skipped + 1)) ;;
    esac
  done < "$STATE_FILE"

  echo "Total: $total"
  echo "Phase 1 (scoring): $scored scored | $score_failed failed | $unavailable unavailable (expired/blocked)"
  echo "Phase 2 (eval):    $evaled evaluated | $eval_failed failed"
  echo "Phase 3 (PDF):     $pdfs PDFs | $pdf_failed failed | $skipped below threshold"

  if (( score_count > 0 )); then
    local avg
    avg=$(echo "scale=1; $score_sum / $score_count" | bc 2>/dev/null || echo "N/A")
    echo "Average Ollama score: $avg/5 ($score_count offers)"
  fi
}

# ── Main ──────────────────────────────────────────────────────────────────────

main() {
  check_prerequisites
  check_ollama

  [[ "$DRY_RUN" == "false" ]] && acquire_lock

  init_state
  reconcile_state

  local total_input
  total_input=$(tail -n +2 "$INPUT_FILE" | grep -c '[^[:space:]]' 2>/dev/null || true)
  total_input="${total_input:-0}"

  if (( total_input == 0 )); then
    echo "No offers in $INPUT_FILE."
    exit 0
  fi

  echo "=== snipe local runner ==="
  local phase3_label="Local Ollama $PHASE3_MODEL (ctx $LOCAL_CTX)"
  local eval_label; eval_label=$([[ "$EVALUATOR_SCRIPT" == "staged-evaluator.mjs" ]] && echo "staged" || echo "classic")
  echo "Phase 1: $OLLAMA_MODEL | Phase 2: $PHASE2_MODEL ($eval_label) | Phase 3: $phase3_label | Threshold: $THRESHOLD${P1_THRESHOLD:+ | P1 gate: $P1_THRESHOLD}"
  echo "Phase 1 parallel: $PARALLEL_SCORE | Phase 2 parallel: $PARALLEL_EVAL | Phase 3 parallel: $PARALLEL_PDF"
  echo "Input: $total_input offers"

  # ── Build offer lists ────────────────────────────────────────────────────────

  local -a phase1_offers=()
  local -a all_offers=()

  while IFS=$'\t' read -r id url source notes; do
    [[ "$id" == "id" || -z "$id" || -z "$url" ]] && continue
    [[ "$id" =~ ^[0-9]+$ ]] || continue
    (( id < START_FROM )) && continue
    [[ -n "$ONLY_ID" && "$id" != "$ONLY_ID" ]] && continue

    local p1_status; p1_status=$(get_field "$id" "p1_status")
    local p2_status; p2_status=$(get_field "$id" "p2_status")
    local p3_status; p3_status=$(get_field "$id" "p3_status")

    # Never retry unavailable postings (expired/blocked) — they won't recover
    [[ "$p1_status" == "unavailable" ]] && continue

    if [[ "$RETRY_FAILED" == "true" ]]; then
      [[ "$p1_status" != "score_failed" && "$p2_status" != "eval_failed" && "$p3_status" != "pdf_failed" ]] && continue
    fi

    if [[ "$SKIP_PHASE1" == "false" ]]; then
      if [[ "$p1_status" != "scored" || "$RETRY_FAILED" == "true" ]]; then
        # Skip score_failed entries that have exhausted retries (without --retry-failed).
        if [[ "$p1_status" == "score_failed" && "$RETRY_FAILED" != "true" ]]; then
          local retries; retries=$(get_field "$id" "retries"); retries=${retries:-0}
          if (( retries >= MAX_RETRIES )); then
            echo "  ⊘  #$id skipped (score_failed, $retries/$MAX_RETRIES retries exhausted)"
            continue
          fi
        fi
        phase1_offers+=("${id}|${url}")
      fi
    fi

    all_offers+=("$id|$url")
  done < "$INPUT_FILE"

  if [[ "$DRY_RUN" == "true" ]]; then
    echo ""
    echo "=== DRY RUN ==="
    echo "Phase 1 (to score): ${#phase1_offers[@]} offers"
    echo "Phase 2 (to evaluate): ${#all_offers[@]} offers"
    exit 0
  fi

  # ── Phase 1 ──────────────────────────────────────────────────────────────────

  if [[ "$SKIP_PHASE1" == "false" && ${#phase1_offers[@]} -gt 0 ]]; then
    run_phase1 "${phase1_offers[@]}"
  elif [[ "$SKIP_PHASE1" == "true" ]]; then
    echo ""; echo "=== Phase 1 skipped (--skip-phase1) ==="
  else
    echo ""; echo "=== Phase 1: no new offers to score ==="
  fi

  # ── Cleanup: reset orphaned evaled offers whose reports are missing ───────────
  # This happens when a previous run assigned a report_num + wrote state=evaled
  # but the report file was never written (e.g., session killed mid-eval).
  # Without this, Phase 3 would fail with "report .md not found" permanently.
  while IFS=$'\t' read -r id url p1s p1sc p1a p2s rnum p3s err ret; do
    [[ "$id" == "id" || -z "$id" ]] && continue
    [[ "$p2s" != "evaled" ]] && continue
    [[ "$rnum" == "-" || -z "$rnum" ]] && continue
    # Check if any report file exists for this report_num
    local report_exists=false
    for rf in "$REPORTS_DIR"/${rnum}-*.md; do
      [[ -f "$rf" ]] && { report_exists=true; break; }
    done
    [[ "$report_exists" == "true" ]] && continue
    # Report missing — also check if eval metadata exists to recover from
    local ef; ef=$(find_eval_file "$id")
    if [[ -n "$ef" ]]; then
      # Eval metadata exists but report is gone — need re-eval
      echo "  ⚠  #$id report $rnum: eval metadata found but report missing — will re-evaluate"
    fi
    update_state "$id" "$url" "$p1s" "$p1sc" "$p1a" "eval_failed" "$rnum" "-" "report missing after eval" "$ret"
  done < "$STATE_FILE"

  # ── Phase 2 ──────────────────────────────────────────────────────────────────

  if [[ "$SKIP_PHASE2" == "true" ]]; then
    echo ""; echo "=== Phase 2 skipped (--skip-phase2) ==="
  else
    local -a phase2_entries=()

    while IFS=$'\t' read -r id url _ _; do
      [[ "$id" == "id" || -z "$id" || -z "$url" ]] && continue
      [[ "$id" =~ ^[0-9]+$ ]] || continue
      (( id < START_FROM )) && continue
      [[ -n "$ONLY_ID" && "$id" != "$ONLY_ID" ]] && continue

      local p1_status p1_score p1_archetype p2_status
      p1_status=$(get_field "$id" "p1_status")
      p1_score=$(get_field "$id" "p1_score")
      p1_archetype=$(get_field "$id" "p1_archetype")
      p2_status=$(get_field "$id" "p2_status")

      [[ "$p1_status" != "scored" ]] && continue
      [[ "$p2_status" == "evaled" && "$RETRY_FAILED" != "true" ]] && continue
      # eval_failed is always retried (up to max attempts) — a stuck eval blocks PDF forever

      # Phase 1 → Phase 2 threshold gate (--p1-threshold)
      if [[ -n "$P1_THRESHOLD" && "$p1_score" != "-" && -n "$p1_score" ]]; then
        if ! (( $(echo "$p1_score >= $P1_THRESHOLD" | bc -l 2>/dev/null || echo 0) )); then
          update_state "$id" "$url" "scored" "$p1_score" "$p1_archetype" "p1-gated" "-" "skipped" "p1-gated(threshold=$P1_THRESHOLD)" "0"
          write_tracker_p1_skip "$id" "$url" "$p1_score" || true
          echo "  ⏭  #$id skipped Phase 2 (P1 score $p1_score < P1 threshold $P1_THRESHOLD, tracker written)"
          continue
        fi
      fi

      phase2_entries+=("${id}|${url}|${p1_score}|${p1_archetype}")
    done < "$INPUT_FILE"

    if [[ ${#phase2_entries[@]} -gt 0 ]]; then
      run_phase2 "${phase2_entries[@]}"
    else
      echo ""; echo "=== Phase 2: no offers to evaluate ==="
    fi
  fi

  # ── Phase 3 ──────────────────────────────────────────────────────────────────

  if [[ "$SKIP_PHASE3" == "true" ]]; then
    echo ""; echo "=== Phase 3 skipped (--skip-phase3) ==="
  else
    local -a phase3_entries=()

    while IFS=$'\t' read -r id url _ _; do
      [[ "$id" == "id" || -z "$id" || -z "$url" ]] && continue
      [[ "$id" =~ ^[0-9]+$ ]] || continue
      (( id < START_FROM )) && continue
      [[ -n "$ONLY_ID" && "$id" != "$ONLY_ID" ]] && continue

      local p1_status p1_score p1_archetype p2_status p2_report_num p3_status
      p1_status=$(get_field "$id" "p1_status")
      p1_score=$(get_field "$id" "p1_score")
      p1_archetype=$(get_field "$id" "p1_archetype")
      p2_status=$(get_field "$id" "p2_status")
      p2_report_num=$(get_field "$id" "p2_report_num")
      p3_status=$(get_field "$id" "p3_status")

      [[ "$p1_status" != "scored" || "$p2_status" != "evaled" ]] && continue
      [[ "$p3_status" == "completed" && "$RETRY_FAILED" != "true" ]] && continue

      # Gate on the Phase 2 eval score (the authoritative, code-computed value),
      # not the Phase 1 pre-screen score. Fall back to p1_score only if the eval
      # JSON is missing or has no score.
      local eval_file gate_score
      eval_file=$(find_eval_file "$id")
      gate_score=""
      [[ -n "$eval_file" ]] && gate_score=$(jq -r '.score // empty' "$eval_file" 2>/dev/null || true)
      [[ -z "$gate_score" || "$gate_score" == "null" ]] && gate_score="$p1_score"

      if [[ "$gate_score" != "-" && -n "$gate_score" ]]; then
        if (( $(echo "$gate_score >= $THRESHOLD" | bc -l) )); then
          phase3_entries+=("${id}|${url}|${p1_score}|${p1_archetype}|${p2_report_num}")
        else
          # Write tracker line directly — no LLM needed for below-threshold offers
          update_state "$id" "$url" "scored" "$p1_score" "$p1_archetype" "evaled" "$p2_report_num" "skipped" "below-threshold($THRESHOLD)" "0"
          write_tracker_skip "$id" "$url" "$p1_score" "$p2_report_num" || true
          echo "  ⏭  #$id skipped (eval score $gate_score < threshold $THRESHOLD, tracker written)"
        fi
      fi
    done < "$INPUT_FILE"

    if [[ ${#phase3_entries[@]} -gt 0 ]]; then
      run_phase3 "${phase3_entries[@]}"
    else
      echo ""; echo "=== Phase 3: no offers above threshold $THRESHOLD ==="
    fi
  fi

  # ── Sync embedding indexes ──────────────────────────────────────────────────────
  # Catches up any JDs fetched this run whose calibration-RAG index write got
  # dropped (e.g. Ollama busy during a phase swap). Incremental — no cache wipe.
  if [[ "$EVALUATOR_SCRIPT" == "staged-evaluator.mjs" ]]; then
    echo ""
    echo "=== Syncing embedding indexes ==="
    node "$BATCH_DIR/embeddings.mjs" sync || echo "⚠  embeddings.mjs sync failed"
  fi

  # ── Merge + verify ────────────────────────────────────────────────────────────

  echo ""
  echo "=== Merging tracker additions ==="
  node "$PROJECT_DIR/tracker/merge-tracker.mjs" || echo "⚠  merge-tracker.mjs failed"

  echo ""
  echo "=== Verifying pipeline integrity ==="
  node "$PROJECT_DIR/tracker/verify-pipeline.mjs" || echo "⚠  Verification found issues (see above)"

  print_summary
}

main "$@"
