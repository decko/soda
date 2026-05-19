# Pipeline metrics

SODA uses [raki](https://github.com/decko/raki) to evaluate pipeline quality
across completed sessions. This guide covers what metrics are available, how to
install raki with full support, and how to run evaluations.

---

## Install

Raki has two tiers of functionality depending on which extras are installed.

**Operational metrics only** (no API key needed):

```bash
uv tool install raki
```

**Full metrics** (operational + knowledge + LLM-judged retrieval quality):

```bash
uv tool install raki \
  --with jinja2 \
  --with ragas \
  --with 'anthropic[vertex]' \
  --with google-genai \
  --with google-cloud-aiplatform
```

The full install requires a Vertex AI or Anthropic API key for the judge metrics.
Operational metrics run locally with no external calls.

---

## Run

```bash
# Operational metrics only
raki run -m raki.yaml

# Full metrics with LLM judge (Vertex Anthropic)
GOOGLE_CLOUD_PROJECT=<your-project> CLOUD_ML_REGION=us-east5 \
  raki run -m raki.yaml \
    --docs-path . \
    --judge \
    --judge-provider vertex-anthropic \
    --judge-model claude-sonnet-4-6

# Validate the manifest without running
raki validate -m raki.yaml

# Show metric trends across runs
raki trends
```

Raki reads completed sessions from `.soda/` (gitignored). You need at least one
completed pipeline run before there is anything to evaluate. Reports are written
to the `results/` directory (also gitignored).

---

## Metric reference

### Operational health

These metrics are derived from session state on disk — no API key needed.

| Metric | What it measures |
|--------|-----------------|
| `first_pass_success_rate` | Fraction of sessions with zero review rework cycles |
| `rework_cycles` | Mean review→implement iterations per session |
| `severity_score` | Weighted severity of review findings (1.0 = no findings) |
| `cost_efficiency` | Mean USD cost per session |
| `self_correction_rate` | Fraction of rework findings the agent resolves itself |
| `phase_execution_time` | Mean total wall-clock time per session (seconds) |
| `tokens_per_phase` | Mean token usage (in + out) per phase |
| `triage_calibration` | Fraction of sessions where triage complexity matches actual cost band |
| `file_prediction_accuracy` | Mean F1 between triage-predicted files and files actually changed |

### Knowledge quality

Requires `--docs-path` pointing at the project root. Measures whether rework
is caused by missing or wrong information in the knowledge base.

| Metric | What it measures |
|--------|-----------------|
| `knowledge_gap_rate` | Fraction of rework findings in domains not covered by the KB |
| `knowledge_miss_rate` | Fraction of rework findings in KB-covered domains that were still wrong |

A `knowledge_miss_rate` near 1.0 means the information exists in the docs but
the agent didn't retrieve or apply it — the fix is better context injection, not
more documentation.

### Retrieval quality (LLM-judged)

Requires `--judge`. Uses an LLM to evaluate how well the agent retrieved and
used relevant context.

| Metric | What it measures |
|--------|-----------------|
| `context_precision` | Relevance of retrieved contexts to the question |
| `context_recall` | Coverage of needed information in retrieved contexts |
| `faithfulness` | Whether the response is faithful to retrieved contexts |
| `answer_relevancy` | Relevance of the generated response to the question |

> **Calibration caveat:** judge metrics are produced by the same provider as the
> pipeline itself. Scores are directional signals, not ground truth.

---

## Interpreting results

**`first_pass_success_rate`** is the primary quality signal. A session "passes"
if it reaches submit with zero review rework cycles. Corrective patches
(verify→patch) do not count as rework.

**`knowledge_miss_rate = 1.0`** means retrieval gaps cause nearly all rework —
the agent can fix the bug once it sees the right code, but didn't retrieve it
during implementation. Context injection improvements (snippet injection, diff
context) directly reduce rework in this regime.

**`severity_score`** below 0.8 indicates the review phase is consistently
finding significant issues. Check whether review prompts are well-calibrated or
whether the implementation phase needs more context.

**`self_correction_rate = 1.0`** is a degenerate signal when rework cycles are
low — it means the agent fixes everything it reworks, but there are so few
rework events that variance is zero.

---

## v0.6.0 baseline (n=6, 2026-05-14)

| Metric | Score |
|--------|-------|
| First-pass success rate | 0.33 |
| Rework cycles | 0.7 |
| Severity score | 0.65 |
| Cost / session | $6.66 |
| Self-correction rate | 1.00 |
| Phase execution time | 1468s |
| Tokens / phase | 15,733 |
| Triage calibration | 1.00 |
| File prediction accuracy | 0.80 |
| Knowledge gap rate | 0.00 |
| Knowledge miss rate | 1.00 |
| Context precision | 0.94 |
| Context recall | 0.70 |
| Faithfulness | 0.34 |
| Answer relevancy | 0.74 |

Notes: first-pass 0.33 is misleading — 3 of the 6 sessions used the corrective
patch loop (verify→patch) rather than review rework. The "no review rework" rate
is 83% (5/6). `knowledge_miss_rate = 1.00` confirms retrieval gaps remain the
primary driver of rework.
