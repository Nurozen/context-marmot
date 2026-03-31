# Language Comparison: ContextMarmot Engine

Evaluated: Go, TypeScript, Elixir, Rust, Python. Date: 2026-03-30.

## Comparison Table

| Criterion | Go | TypeScript | Rust | Elixir | Python |
|---|---|---|---|---|---|
| **MCP SDK** | Official (`modelcontextprotocol/go-sdk`, Google co-maintained). v1 stable. Also `mark3labs/mcp-go`. | Official (`@modelcontextprotocol/sdk`). Most mature SDK. Reference implementation. | Official (`rmcp` v0.16). Actively maintained, async/Tokio. | Community (`hermes_mcp` v0.8, `vancouver`). No official SDK. | Official (`mcp` PyPI). FastMCP integrated. Most tutorials/examples. |
| **sqlite-vec** | CGo via `asg017/sqlite-vec-go-bindings/cgo` + `mattn/go-sqlite3`. WASM via `ncruces/go-sqlite3` (no CGo, easy cross-compile). Both first-party maintained. | `better-sqlite3` + loadable extension. Works but native addon complicates distribution. | `rusqlite` + `sqlite-vec` crate. First-class FFI. Best integration story. | `ecto_sqlite3` + `sqlite_vec` hex package. Precompiled NIF from GitHub releases. Works but thinnest ecosystem. | `sqlite-vec` pip install. First-class support (Alex Garcia's primary target). Easiest setup. |
| **YAML parsing** | `gopkg.in/yaml.v3` (standard, mature) | `js-yaml`, `yaml` (npm). Mature. | `serde_yaml`. Mature, idiomatic with serde. | `:yamerl` or `yaml_elixir`. Adequate. | `PyYAML`, `ruamel.yaml`. Mature. |
| **Markdown parsing** | `goldmark` (CommonMark, extensible). `yuin/goldmark`. | `unified`/`remark` ecosystem. Best-in-class. | `pulldown-cmark`. Fast, CommonMark compliant. | `earmark`. Adequate. | `markdown-it-py`, `mistune`. Mature. |
| **File watching** | `fsnotify/fsnotify`. De facto standard. Cross-platform. | `chokidar` (node), `Deno.watchFs`, `fs.watch`. Mature. | `notify` crate. Cross-platform, async. | `file_system` hex. Wraps native FS listeners. Works. | `watchdog`. Mature, cross-platform. |
| **Concurrency** | Goroutines + channels + `sync.RWMutex`. Excellent for concurrent file I/O. Simple model. | Single-threaded event loop. Concurrent I/O via async but no true parallelism without workers. Sufficient for I/O-bound. | `tokio` async + `Arc<RwLock>`. Highest safety guarantees. Most complex to write. | OTP processes + GenServers + supervision trees. Best fault tolerance. Natural fit for daemon. | `asyncio` or threading. GIL limits true parallelism. Adequate for I/O-bound only. |
| **HTTP/WebSocket** | `net/http` (stdlib) + `gorilla/websocket` or `nhooyr/websocket`. No framework needed. | Express/Fastify + `ws`, or Deno/Bun built-in. Excellent. | `axum` + `tokio-tungstenite`. High performance. | Phoenix + Phoenix.Channels. Best WebSocket story (originally built for real-time). | FastAPI + `websockets`. Good. |
| **Distribution** | **Single static binary.** `go build`. Zero dependencies. Cross-compile trivial (WASM path) or needs C toolchain (CGo path). | Deno: `deno compile` single binary. Bun: `bun build --compile`. Node: needs `pkg` or Docker. Native SQLite addons complicate all paths. | **Single static binary.** `cargo build --release`. Smallest output. Cross-compile via `cross`. | Burrito/Bakeware: single binary possible but bundles BEAM VM (~40-80MB). Mix releases more common. Requires Erlang on target or bundled. | PyInstaller/Nuitka: fragile with native deps. Practical path is `pip install` or Docker. Not a real single binary. |
| **Ecosystem alignment** | Official Anthropic Go SDK. LangChainGo exists but thin. Not the primary AI ecosystem. | Primary AI agent ecosystem (Claude Code, Cursor, Vercel AI SDK). Best library coverage. | No official Anthropic SDK. Community crates. AI ecosystem is nascent. | No official Anthropic SDK. Community `anthropix`. AI ecosystem minimal. | Primary AI/ML ecosystem. Official Anthropic SDK. LangChain, LlamaIndex, everything. |
| **Developer velocity** | High. Fast compile. Simple language. Less boilerplate than Rust, more than Python. | Highest for prototyping. Most AI examples/tutorials. But type safety gaps at runtime. | Lowest. Borrow checker, lifetimes. Highest correctness but 2-3x dev time. | Moderate. Pattern matching is expressive but OTP learning curve. Small talent pool. | Highest for prototyping. But type safety and distribution story are weak. |
| **Runtime performance** | ~10ms startup. ~30MB RSS. Excellent file I/O throughput. GC pauses negligible for this workload. | ~50-150ms startup (Deno/Bun). ~60-100MB RSS. V8 overhead. Good enough. | ~2ms startup. ~10MB RSS. Best raw performance. Overkill for this workload. | ~300-500ms startup (BEAM boot). ~80-120MB RSS. Excellent sustained throughput. Startup is the weakness. | ~100ms startup. ~50MB RSS. Adequate. Slowest for CPU-bound (CRUD classification). |
| **LLM API clients** | Official `anthropics/anthropic-sdk-go`. `sashabaranov/go-openai`. | Official Anthropic + OpenAI SDKs. Best coverage. | Community only (`anthropic-sdk-rust`, `async-openai`). | Community only (`anthropix`). | Official Anthropic + OpenAI SDKs. Best coverage. |

## Elixir Deep Dive

- **OTP supervision trees**: Natural fit for daemon (restart file watcher, reconnect SQLite, isolate agent sessions). Best fault-tolerance story of any option.
- **ETS/DETS**: In-memory graph state in ETS is extremely fast for read-heavy workloads. DETS provides disk persistence. Could avoid SQLite for graph traversal entirely.
- **Phoenix Channels**: Best WebSocket implementation. Built for thousands of concurrent connections. Overkill but excellent.
- **SQLite/sqlite-vec**: `ecto_sqlite3` works. `sqlite_vec` hex wraps precompiled binaries. Thinnest integration -- fewer examples, fewer battle-tested deployments.
- **Single binary**: Burrito produces cross-platform binaries but bundles the entire BEAM VM (40-80MB+). Adds build complexity. Not as clean as Go/Rust.
- **Verdict**: Architecturally ideal (OTP is purpose-built for this kind of daemon), but ecosystem gaps in AI tooling, sqlite-vec maturity, and distribution make it a risky choice for a solo/small team.

## Other Languages Considered

- **Zig**: No MCP SDK. No sqlite-vec bindings. Too immature for application development (stdlib still unstable). Hard no.
- **OCaml**: No MCP SDK. Tiny ecosystem. Single binary via `dune` but library gaps everywhere.
- **Kotlin/JVM**: Official MCP Java SDK. Excellent libraries. But JVM distribution (GraalVM native-image is fragile with SQLite JNI). Startup overhead. Not competitive with Go's simplicity.
- **C#/.NET**: Official MCP SDK. Good SQLite story. NativeAOT for single binary. But ecosystem misaligned with AI agent tooling. Not a contender.

## Recommendation

**Go is the correct choice.** Rationale by elimination:

1. **TypeScript** is the closest competitor (best MCP SDK, best AI ecosystem) but native SQLite bindings make single-binary distribution painful. `better-sqlite3` is a native addon that must be rebuilt per platform. Deno compile helps but sqlite-vec integration is unproven at scale.

2. **Python** has the best AI ecosystem but cannot produce a reliable single binary with native SQLite deps. Distribution is the fatal flaw for a tool that agents install and run as a daemon.

3. **Rust** produces the best binaries but 2-3x development time and no official Anthropic SDK make it wrong for a project that needs to iterate fast on AI integration patterns.

4. **Elixir** has the best daemon architecture (OTP) but the thinnest AI ecosystem, smallest sqlite-vec community, and Burrito distribution adds friction.

5. **Go** uniquely satisfies all hard requirements: official MCP SDK (Google co-maintained), official Anthropic SDK, single binary with zero dependencies (WASM path avoids CGo entirely), sqlite-vec first-party bindings, goroutine concurrency, fast startup (~10ms), and sufficient developer velocity. The only trade-off is it's not the primary AI ecosystem language -- but ContextMarmot is infrastructure consumed *by* AI agents, not an AI library itself.

**Key Go architecture note**: Use `ncruces/go-sqlite3` (WASM, no CGo) + `asg017/sqlite-vec-go-bindings/ncruces` for zero-CGo builds. This enables trivial cross-compilation and produces truly static binaries.

Sources:
- [Official Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk)
- [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go)
- [Official TypeScript MCP SDK](https://github.com/modelcontextprotocol/typescript-sdk)
- [Official Rust MCP SDK (rmcp)](https://github.com/modelcontextprotocol/rust-sdk)
- [Hermes MCP for Elixir](https://github.com/cloudwalk/hermes-mcp)
- [FastMCP Python](https://github.com/prefecthq/fastmcp)
- [sqlite-vec](https://github.com/asg017/sqlite-vec)
- [sqlite-vec Go bindings](https://github.com/asg017/sqlite-vec-go-bindings)
- [sqlite-vec Go docs](https://alexgarcia.xyz/sqlite-vec/go.html)
- [sqlite-vec Rust docs](https://alexgarcia.xyz/sqlite-vec/rust.html)
- [rusqlite](https://github.com/rusqlite/rusqlite)
- [Elixir sqlite_vec wrapper](https://github.com/joelpaulkoch/sqlite_vec)
- [ncruces/go-sqlite3 (WASM, no CGo)](https://github.com/ncruces/go-sqlite3)
- [Anthropic Client SDKs](https://platform.claude.com/docs/en/api/client-sdks)
- [Anthropic Go SDK](https://github.com/anthropics/anthropic-sdk-go)
- [Burrito (Elixir binary distribution)](https://github.com/burrito-elixir/burrito)
- [Bakeware](https://github.com/bake-bake-bake/bakeware)
- [Deno compile docs](https://docs.deno.com/runtime/reference/cli/compile/)
- [Elixir file_system](https://github.com/falood/file_system)
- [MCP Specification](https://modelcontextprotocol.io/specification/2025-06-18)
