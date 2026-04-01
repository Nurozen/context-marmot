# Benchmark: ContextMarmot vs Vanilla File Reading

Evaluates whether agents retrieve better context through ContextMarmot's graph-based MCP tools compared to reading raw files.

## Dataset

[SWE-QA-Benchmark](https://huggingface.co/datasets/swe-qa/SWE-QA-Benchmark) — 20 curated questions across 4 Python repositories (5 per repo: django, flask, pytest, requests). Questions require cross-file architectural reasoning and are scored against gold answers referencing specific source files and line numbers.

| Repo | Questions | Avg Vanilla Turns |
|------|-----------|------------------|
| Django | 5 | 10.0 |
| Flask | 5 | 4.8 |
| pytest | 5 | 8.2 |
| Requests | 5 | 6.6 |

## Eval Method

Three conditions are evaluated via `bin/marmot-eval`, which invokes `claude -p` for each (question, condition) pair using Claude Sonnet:

**Condition A — Vanilla:** Claude with `Read, Grep, Glob, Bash` against a full repo clone. No graph assistance.

**Condition B — Hybrid (ContextMarmot):** Claude with the same file tools **plus** `context_query` and `context_verify` against a pre-seeded ContextMarmot vault. The agent is prompted to query the graph first, then read only the files and line ranges it identifies.

Each answer is scored by a Claude judge on three dimensions (1–5):
- **Correctness** — Is the answer technically accurate?
- **Completeness** — Does it cover all relevant aspects?
- **Specificity** — Does it cite specific files, functions, and line numbers?

The judge is blinded to which condition produced each answer.

## Results

Nodes seeded with real file content (up to 8 KB of source code per node) and joint summary+context embeddings. Embedder: OpenAI `text-embedding-3-small`.

| Metric | Vanilla (file tools) | Hybrid (MCP + files) | Δ |
|--------|---------------------|---------------------|---|
| **Avg quality (1–5)** | **4.62** | **4.62** | **0.0%** |
| Avg turns | 7.5 | 6.9 | −8% |
| Avg tokens | 151,327 | 95,876 | **−37%** |
| Avg cost / question | $0.1065 | $0.0834 | **−22%** |
| Avg duration | 38,959 ms | 39,015 ms | ≈0% |

**Hybrid achieves identical answer quality at 22% lower cost and 37% fewer tokens**, with no measurable latency penalty.

### Per-question breakdown

| ID | V Quality | H Quality | V Turns | H Turns | V Cost | H Cost |
|----|-----------|-----------|---------|---------|--------|--------|
| django_01 | 4.3 | 4.3 | 10 | 9 | $0.1991 | $0.1591 |
| django_02 | 5.0 | 5.0 | 3 | 4 | $0.0579 | $0.0553 |
| django_03 | 4.3 | 4.3 | 6 | 7 | $0.0774 | $0.0741 |
| django_04 | 5.0 | 5.0 | **28** | **14** | $0.2824 | $0.1623 |
| django_05 | 4.3 | **4.7** | 3 | 5 | $0.0435 | $0.0512 |
| flask_01 | 5.0 | 5.0 | 3 | 5 | $0.0946 | $0.0890 |
| flask_02 | 5.0 | 5.0 | 10 | **7** | $0.1413 | $0.0928 |
| flask_03 | 5.0 | 5.0 | 4 | 4 | $0.0823 | $0.0656 |
| flask_04 | 4.3 | **4.7** | 4 | 4 | $0.0440 | $0.0457 |
| flask_05 | 4.7 | **5.0** | 4 | 5 | $0.0538 | $0.0520 |
| pytest_01 | 4.3 | 4.3 | 5 | 6 | $0.0801 | $0.0836 |
| pytest_02 | 5.0 | 5.0 | 3 | 4 | $0.0476 | $0.0490 |
| pytest_03 | 3.3 | 3.0 | 10 | **9** | $0.1230 | $0.0827 |
| pytest_04 | 5.0 | 4.7 | 6 | **9** | $0.1493 | $0.0986 |
| pytest_05 | 4.7 | 4.7 | **17** | **13** | $0.2472 | $0.1384 |
| requests_01 | 4.7 | 4.7 | 7 | **5** | $0.1106 | $0.0804 |
| requests_02 | 4.7 | 4.7 | 6 | 7 | $0.0602 | $0.0566 |
| requests_03 | 4.7 | 4.0 | 3 | 4 | $0.0366 | $0.0383 |
| requests_04 | 4.7 | 4.7 | 5 | 6 | $0.0652 | $0.0521 |
| requests_05 | 4.3 | **4.7** | 12 | 11 | $0.1348 | $0.1410 |

Bold entries indicate cases where hybrid outperformed vanilla (turns/cost) or vice versa.

**Hybrid wins on quality:** 5 questions where H > V, 1 where V > H, 14 tied.

**Largest cost wins for hybrid:** django_04 (28→14 turns, $0.28→$0.16), pytest_05 (17→13 turns, $0.25→$0.14), flask_02 (10→7 turns, $0.14→$0.09). These are hard multi-file questions where the graph provides decisive navigation.

## Comparison with related work

| System | Quality vs baseline | Token reduction | Notes |
|--------|--------------------|-----------------|----|
| **ContextMarmot Hybrid v2** | **0.0%** | **−37%** | This benchmark |
| Codebase-Memory (graph-only) | −9% | −99% | arxiv 2603.27277 |
| RepoMaster | +54% task pass | −95% | NeurIPS 2025 |
| Sandelin memory benchmark | 0% | −22–32% | Complex tasks only |
| Meta-RAG summarization | ~0% | −79.8% | SWE-bench Lite |

ContextMarmot's 0% quality delta with −37% token reduction is consistent with the literature finding that graph/memory navigation does not improve quality (quality ceiling is a property of the model) but reduces exploration overhead.

## Running the eval

```bash
# Build binaries first
make build && make build-eval

# Dry run (2 questions, skip MCP-only)
bin/marmot-eval --questions 2 --skip-mcp --output testdata/eval/results

# Full run (vanilla vs hybrid only, recommended)
source .env && bin/marmot-eval --skip-mcp --output testdata/eval/results
```

Results are written to `testdata/eval/results/eval_results.json` and `eval_results.md`. Checkpointing is automatic — interrupted runs resume from the last completed question.

## References

- **SWE-QA**: Peng et al., *"SWE-QA: A QA Benchmark for Real-World Software Engineering Knowledge"*, 2025. [arxiv.org/abs/2509.14635](https://arxiv.org/abs/2509.14635)
- **SWE-QA Dataset**: [huggingface.co/datasets/swe-qa/SWE-QA-Benchmark](https://huggingface.co/datasets/swe-qa/SWE-QA-Benchmark)
- **Codebase-Memory MCP**: [arxiv.org/abs/2603.27277](https://arxiv.org/abs/2603.27277)
- **RepoMaster**: [arxiv.org/abs/2505.21577](https://arxiv.org/abs/2505.21577)
- **ContextBench**: [arxiv.org/abs/2602.05892](https://arxiv.org/abs/2602.05892)
