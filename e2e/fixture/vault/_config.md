---
version: "1"
namespace: default
embedding_provider: mock
token_budget: 8192
---
# ContextMarmot E2E Fixture Vault

Static fixture used by the e2e suite. Tests copy this directory to a temp
location before running, so it is never mutated in place.
