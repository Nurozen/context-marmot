# Embedding Providers

ContextMarmot supports pluggable embedding providers for semantic search. Out of the box, two providers are available:

| Provider | Models | Description |
|----------|--------|-------------|
| `mock` (default) | `mock-v1` | Character trigram hashing. No API key needed. Lexical overlap only --- texts must share substrings to match. |
| `openai` | `text-embedding-3-small` (default), `text-embedding-3-large`, `text-embedding-ada-002` | Real semantic embeddings via the OpenAI API. Paraphrases and synonyms match. |

## Configuring OpenAI embeddings

The easiest way is the interactive configure command (also runs automatically during `marmot init`):

```bash
marmot configure
```

This prompts for provider, model, and API key. The key is stored in `.marmot/.marmot-data/.env` (gitignored).

**Manual configuration** --- edit `.marmot/_config.md` frontmatter directly:

```yaml
---
version: "1"
namespace: default
embedding_provider: openai
embedding_model: text-embedding-3-small
---
```

Then set your API key via environment variable:

```bash
export OPENAI_API_KEY=sk-...
```

After changing the embedding provider or model, rebuild embeddings:

```bash
marmot index --force
```

## Fallback behavior

If `embedding_provider` is set to `openai` (or another non-mock provider) but the required API key is not found in the environment, ContextMarmot logs a warning to stderr and falls back to the mock embedder automatically. This ensures the tool never fails to start due to a missing key.
