# Hivemind Brain

A [LangGraph](https://langchain-ai.github.io/langgraph/) reasoning service for
Hivemind incident triage.

The Go operator owns the Kubernetes control plane — it watches `IncidentTriage`
CRs, gathers raw evidence (pod logs, Prometheus trends, runbooks), and opens
remediation PRs. This service owns the **reasoning**: a *cyclic* graph that
synthesizes a root-cause hypothesis, critiques its own confidence, and loops to
re-examine the evidence until it is confident or hits an iteration cap.

That self-correcting loop is the reason this layer is a LangGraph `StateGraph`
rather than the operator's `errgroup` fan-out — a fixed DAG can't express the
`critique → gather` back-edge.

## The graph

```
START → recall → gather → synthesize → critique → ┐
                   ▲                               │ route_after_critique
                   └──────────── loop ─────────────┤   (confidence < threshold
                                 done → approval → finalize → remember → END
                                          │
                                     interrupt() ⏸  (only when require_approval)
```

| Node         | Role                                                              |
|--------------|-------------------------------------------------------------------|
| `recall`     | Retrieve similar past incidents from memory to prime synthesis    |
| `gather`     | Distill evidence per source — one summary pass, or a tool-choosing ReAct investigation (see below) |
| `synthesize` | Combine the evidence (+ recalled incidents) into one root-cause hypothesis + fix |
| `critique`   | Score confidence (0–1) and emit guidance for another pass          |
| `approval`   | Human-in-the-loop gate: `interrupt()`s for a decision when asked   |
| `finalize`   | Assemble the report returned to the caller                         |
| `remember`   | Store this finalized incident in memory for future recall          |

(`recall`/`remember` are present only when memory is enabled.)

The state schema, reducers, and edges live in
[`hivemind_brain/`](hivemind_brain/).

## Human-in-the-loop approval

The `approval` node is the second thing a fixed DAG can't do. With
`require_approval`, the graph reaches a proposed root cause, then **`interrupt()`s** —
it checkpoints its full state and pauses. `/triage` returns the pending proposal
and a `thread_id`; a human resumes it later with `/resume`, and the graph
continues from exactly where it stopped. That durable, resumable pause is what a
plain function chain can't provide.

```sh
# 1) Gated triage — pauses, returns a thread_id + the proposal for review.
curl -s localhost:8090/triage -H 'content-type: application/json' -d '{
  "alert":"OOMKilled","logs":"OOMKilled; restarted 5x","require_approval":true
}'   # → {"status":"awaiting_approval","thread_id":"…","approval_request":{…}}

# 2) A human approves (or rejects) — the graph resumes and finalizes.
curl -s localhost:8090/resume -H 'content-type: application/json' -d '{
  "thread_id":"…","action":"approve","note":"lgtm"
}'   # → {"status":"completed","approved":true,"root_cause":"…"}
```

State is held by an in-memory checkpointer, so the brain runs single-replica
(`brain.enabled` deploys one). A multi-replica brain would swap in a shared
checkpointer (Postgres/Redis). The default `/triage` (no `require_approval`) is
unchanged and still completes in one call.

## Incident memory

The graph is bookended with a vector store of past incidents ([`memory.py`](hivemind_brain/memory.py)):
a `recall` node retrieves the most similar past incidents before `gather` and
feeds them into the `synthesize` prompt, and a `remember` node stores each
finalized incident after `finalize`. So recurring failure modes converge on what
worked before — the brain gets better over time instead of starting cold every
alert. Rejected proposals are not remembered.

Embeddings are deterministic **feature hashing** (real cosine-similarity vectors,
no external embedding model or API — Groq offers none), so it works everywhere
offline; swap in a semantic embedding model for nuance. The store is in-memory
and single-replica (like the approval checkpointer); `/healthz` reports its size.
Disable with `HIVEMIND_MEMORY_ENABLED=false` (or `brain.memoryEnabled: false`);
tune recall breadth with `HIVEMIND_MEMORY_K`.

## Evidence gathering: summary vs ReAct

The `gather` node has two modes (`HIVEMIND_GATHER_MODE`, or `brain.gatherMode`):

- **`summary`** (default) — one LLM pass distills the whole evidence bundle.
- **`react`** — a tool-choosing agent ([`tools.py`](hivemind_brain/tools.py))
  investigates the bundle instead of summarizing it blind: each step it decides
  which tool to call (`search_logs`, `get_metrics`, `list_runbooks`,
  `read_runbook`) over the POSTed evidence, accumulating observations until it
  has enough or hits `HIVEMIND_REACT_MAX_STEPS`. The trace lands in the node's
  history.

ReAct uses **JSON actions** rather than native tool-calling, so it works with any
chat model — including the offline mock — and needs no tool-calling support from
the provider. (The brain has no cluster access, so the tools query the evidence
the operator already gathered, not the live cluster.)

## Run it

```sh
cd brain
python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt

cp .env.example .env   # add your GROQ_API_KEY, or leave provider on the mock
python -m hivemind_brain.server   # serves on :8090
```

Then:

```sh
curl -s localhost:8090/healthz
curl -s localhost:8090/triage -H 'content-type: application/json' -d '{
  "alert": "OOMKilled", "namespace": "prod", "pod": "checkout-7d9",
  "logs": "OOMKilled; restarted 5x", "metrics": "memory climbing to limit",
  "runbooks": [{"name": "OOMKill", "content": "raise limits; find the leak"}]
}' | python -m json.tool
```

## LLM backend

Provider resolves from `HIVEMIND_LLM_PROVIDER`:

- `auto` (default) — Groq if `GROQ_API_KEY` is set, otherwise the mock.
- `groq` — LangChain's `ChatGroq` (OpenAI-compatible, fast free tier).
- `mock` — a deterministic model that returns valid per-node JSON and exercises
  one reflection loop. No key, no network — this is what the tests use.

## Tests

```sh
cd brain && source .venv/bin/activate
HIVEMIND_LLM_PROVIDER=mock pytest
```

## Status & roadmap

**Wired in.** The operator calls this service through its `Reasoner` seam
([`internal/reasoner`](../internal/reasoner/)): set `HIVEMIND_REASONER_URL` to
this service's base URL and the Triaging phase's synthesis step POSTs to
`/triage` instead of running the in-process synthesizer. The operator still
gathers the evidence and passes it in; the brain runs the reflection loop.

**Deployable in-cluster.** The chart ships the brain as its own
Deployment + Service (`brain.enabled`, on by default) and wires the operator's
`HIVEMIND_REASONER_URL` at it automatically:

```sh
make docker-build-brain docker-push-brain      # build + push ghcr.io/yih0nk/hivemind-brain
helm upgrade --install hivemind ./charts/hivemind \
  --set llmApiKey=$GROQ_API_KEY --set brain.provider=groq
```

The brain reuses the operator's `llmApiKey` as its `GROQ_API_KEY`. Set
`brain.enabled=false` to fall back to the operator's in-process synthesizer.

Run it standalone for local dev with `python -m hivemind_brain.server` (above).
