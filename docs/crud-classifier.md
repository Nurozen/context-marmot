# CRUD Classifier

When an agent calls `context_write`, ContextMarmot automatically classifies the write before persisting:

| Action | Meaning |
|--------|---------|
| **ADD** | New concept --- no similar node exists |
| **UPDATE** | Enriches or corrects an existing node (same concept, better content) |
| **SUPERSEDE** | Replaces an existing node --- old node is soft-deleted, chain preserved |
| **NOOP** | Content is essentially identical to an existing node --- skipped |

Classification uses embedding similarity to find candidates, then optionally an LLM to decide. Without an LLM, pure embedding distance thresholds are used (NOOP >= 0.95, UPDATE >= 0.80, SUPERSEDE >= 0.65, else ADD).

## Configuring the classifier

Run `marmot configure` (also runs during `marmot init`) --- it prompts for classifier provider after the embedding section:

```
CRUD Classifier:
  > 1) openai   (gpt-5.1-codex-mini)
    2) anthropic (claude-haiku-4-5-20251001)
    3) none     (embedding-distance fallback)
```

If you select **openai** and your embedding provider is also OpenAI, the same `OPENAI_API_KEY` is reused --- no second key needed. Selecting **anthropic** prompts for `ANTHROPIC_API_KEY` separately. Selecting **none** uses the embedding-distance fallback with no API calls.

**Manual configuration** --- edit `.marmot/_config.md` frontmatter directly:

```yaml
classifier_provider: openai
classifier_model: gpt-5.1-codex-mini
```

## Fallback behavior

If the configured classifier provider's API key is not found at serve time, ContextMarmot logs a warning to stderr and falls back to embedding-distance classification automatically.
