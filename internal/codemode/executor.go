// Package codemode implements a sandboxed JavaScript runtime that lets the
// curator chat LLM emit code which queries the graph engine. The runtime is
// goja-based (pure Go ES5.1+), exposes a synchronous `client` global that
// mirrors the SDK's read methods, and enforces wall-clock + result-size
// limits.
package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/nurozen/context-marmot/internal/curator"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
)

const (
	// DefaultTimeout caps wall-clock time for a single code execution.
	DefaultTimeout = 5 * time.Second
	// MaxCodeLength is the maximum number of bytes accepted in a single code
	// block. Anything larger is rejected before execution.
	MaxCodeLength = 4096
	// MaxResultBytes is the JSON-serialized cap for the return value handed
	// back to the second-phase LLM call.
	MaxResultBytes = 8192
	// MaxLogEntries caps captured console.* output.
	MaxLogEntries = 50
	// MaxLogEntryBytes truncates each individual log entry.
	MaxLogEntryBytes = 1024
	// MaxMutationsPerTurn caps how many graph mutations one code execution
	// may perform. Past this, calls to write methods throw a JS exception
	// and the partial result is returned.
	MaxMutationsPerTurn = 50
)

// Result is the outcome of a single code execution.
type Result struct {
	Code       string   // the source executed (post-extraction)
	Value      any      // JSON-marshalable return value, may be nil
	Logs       []string // captured console.* output
	Error      string   // execution error if any
	DurationMS int64
	// Truncated indicates the JSON serialization of Value exceeded
	// MaxResultBytes and was truncated for the next LLM call.
	Truncated bool
	// Mutations records every successful graph mutation performed during
	// this execution. Populated only when the executor was given a
	// non-nil WriteContext.
	Mutations []curator.MutationRecord
}

// WriteContext bundles the per-turn state required to perform mutations from
// code-mode. When non-nil and ReadOnly is false, the `client` global includes
// tag/untag/type/link/unlink/merge/delete methods.
type WriteContext struct {
	SessionID     string
	SelectedNodes []string
	Namespace     string
	UndoStack     *curator.UndoStack
	// NotifyChange, if non-nil, is invoked after every successful mutation
	// so SSE subscribers can refresh.
	NotifyChange func()
	// ReadOnly, when true, suppresses write method registration even though
	// a WriteContext was provided. Set this from VaultConfig.ReadOnly (once
	// feat/package-docs lands) or from `marmot serve --read-only`.
	ReadOnly bool
}

// Executor wraps a graph engine in a goja sandbox. Each call to Execute
// constructs a fresh runtime — there is no state shared between executions.
type Executor struct {
	engine  *mcpserver.Engine
	timeout time.Duration
}

// runScope holds the per-execution state shared between client API methods.
// Created fresh for each Execute call.
type runScope struct {
	rt           *goja.Runtime
	engine       *mcpserver.Engine
	write        *WriteContext // nil = read-only
	ctx          context.Context
	mutations    []curator.MutationRecord
	mutationsCap int
	// allowBulk is set when the generated program calls client.allowBulk(),
	// lifting the bulk-mutation guard for this execution only.
	allowBulk bool
	// mutatedIDs tracks the distinct node IDs successfully mutated during
	// this execution; the bulk-mutation guard counts against it.
	mutatedIDs map[string]struct{}
}

// NewExecutor builds an Executor backed by the given engine. Engine must be
// non-nil; nil engines panic on first client call rather than silently
// returning empty data.
func NewExecutor(engine *mcpserver.Engine) *Executor {
	return &Executor{engine: engine, timeout: DefaultTimeout}
}

// WithTimeout returns a copy of the executor with a custom timeout.
func (e *Executor) WithTimeout(d time.Duration) *Executor {
	cp := *e
	cp.timeout = d
	return &cp
}

// Execute runs jsCode in a fresh, read-only sandbox.
func (e *Executor) Execute(ctx context.Context, jsCode string) *Result {
	return e.ExecuteWithWrites(ctx, jsCode, nil)
}

// ExecuteWithWrites runs jsCode in a fresh sandbox. When write is non-nil
// and the engine is not read-only, write-side client methods (tag, link,
// delete, etc.) are exposed and apply real mutations through the standard
// curator command pipeline (snapshots, undo stack, file writes). Passing
// nil for `write` matches Execute's read-only behavior.
func (e *Executor) ExecuteWithWrites(ctx context.Context, jsCode string, write *WriteContext) *Result {
	start := time.Now()
	res := &Result{Code: jsCode}

	if len(jsCode) > MaxCodeLength {
		res.Error = fmt.Sprintf("code exceeds maximum length (%d > %d bytes)", len(jsCode), MaxCodeLength)
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}

	rt := goja.New()
	rt.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))

	// Disable dangerous globals BEFORE registering anything else.
	disableUnsafe(rt)

	// Register console — captures up to MaxLogEntries lines.
	logs := make([]string, 0, 8)
	registerConsole(rt, &logs)

	// Wrap user code in an IIFE so `return` works at top level.
	wrapped := "(function(){\n" + jsCode + "\n})()"

	// Set up timeout context BEFORE registering the client so per-method
	// Go-side calls (e.g. ExecuteCommand from a code-mode write) can honor
	// the same deadline as the JS execution.
	timeout := e.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Register `client` proxy. Nil engine is permitted for sandbox-only
	// scenarios (tests, dry-run prompts) — `client` is simply absent.
	scope := &runScope{
		rt:           rt,
		engine:       e.engine,
		write:        write,
		ctx:          tctx,
		mutationsCap: MaxMutationsPerTurn,
	}
	if e.engine != nil {
		if err := registerClient(rt, scope); err != nil {
			res.Error = "failed to wire client API: " + err.Error()
			res.DurationMS = time.Since(start).Milliseconds()
			return res
		}
	}

	timer := time.AfterFunc(timeout, func() { rt.Interrupt("execution timeout") })
	defer timer.Stop()

	// Honor an early outer-context cancel too.
	go func() {
		<-tctx.Done()
		if errors.Is(tctx.Err(), context.DeadlineExceeded) {
			rt.Interrupt("execution timeout")
		} else if tctx.Err() != nil {
			rt.Interrupt("cancelled")
		}
	}()

	value, runErr := rt.RunString(wrapped)
	res.DurationMS = time.Since(start).Milliseconds()
	res.Logs = trimLogs(logs)
	res.Mutations = scope.mutations

	if runErr != nil {
		// Distinguish interrupts from regular exceptions for a friendlier message.
		var interrupt *goja.InterruptedError
		if errors.As(runErr, &interrupt) {
			res.Error = fmt.Sprintf("execution timeout after %dms", res.DurationMS)
			return res
		}
		res.Error = simplifyError(runErr.Error())
		return res
	}

	if value == nil || goja.IsUndefined(value) || goja.IsNull(value) {
		return res
	}

	// Export to a Go value, then check size.
	exported := value.Export()

	// Size-check via JSON serialization. The check serves two purposes:
	//  - If the value can't be JSON-encoded at all (channels, funcs, cycles),
	//    we can't reliably hand it to the phase-2 LLM call. Set an error.
	//  - If it serializes too large, replace with a truncated string so
	//    downstream JSON marshal stays bounded.
	blob, err := json.Marshal(exported)
	if err != nil {
		res.Error = "result is not JSON-serializable: " + err.Error()
		return res
	}
	if len(blob) > MaxResultBytes {
		res.Truncated = true
		res.Value = string(blob[:MaxResultBytes]) + "<TRUNCATED at 8KB>"
		return res
	}
	res.Value = exported
	return res
}

// disableUnsafe removes globals that could break sandbox guarantees.
func disableUnsafe(rt *goja.Runtime) {
	for _, name := range []string{
		"eval", "Function",
		"setTimeout", "setInterval", "clearTimeout", "clearInterval",
		"setImmediate", "queueMicrotask", "fetch", "XMLHttpRequest",
	} {
		_ = rt.GlobalObject().Set(name, goja.Undefined())
	}
}

// registerConsole adds a `console` global with log/info/warn/error/debug
// methods that all append to the provided logs slice.
func registerConsole(rt *goja.Runtime, logs *[]string) {
	console := rt.NewObject()
	for _, level := range []string{"log", "info", "warn", "error", "debug"} {
		lvl := level
		_ = console.Set(lvl, func(call goja.FunctionCall) goja.Value {
			if len(*logs) >= MaxLogEntries {
				return goja.Undefined()
			}
			parts := make([]string, 0, len(call.Arguments))
			for _, a := range call.Arguments {
				parts = append(parts, formatLogArg(a))
			}
			line := lvl + ": " + strings.Join(parts, " ")
			if len(line) > MaxLogEntryBytes {
				line = line[:MaxLogEntryBytes] + "...<truncated>"
			}
			*logs = append(*logs, line)
			return goja.Undefined()
		})
	}
	_ = rt.GlobalObject().Set("console", console)
}

func formatLogArg(v goja.Value) string {
	if v == nil {
		return "undefined"
	}
	exp := v.Export()
	if exp == nil {
		return "null"
	}
	switch s := exp.(type) {
	case string:
		return s
	}
	if blob, err := json.Marshal(exp); err == nil {
		return string(blob)
	}
	return v.String()
}

func trimLogs(logs []string) []string {
	if len(logs) == 0 {
		return nil
	}
	if len(logs) > MaxLogEntries {
		return logs[:MaxLogEntries]
	}
	return logs
}

func simplifyError(s string) string {
	// Collapse goja's wrapping ("GoError: ", "TypeError: " etc) into a single
	// concise line. Keep first line only — goja appends a stack which the
	// LLM doesn't need.
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	return s
}

// codeBlockRE matches the first fenced code block whose language tag is
// js/javascript/ts/typescript (case-insensitive), or no tag at all if the
// content looks like JS (contains `client.` or `return ` or `const `/`let `).
var codeBlockRE = regexp.MustCompile("```(?i)(js|javascript|ts|typescript)?\\s*\\n([\\s\\S]*?)```")

// ExtractCode pulls the first JS-looking fenced code block from an LLM
// message, or returns "" if none is present. The language tag is stripped
// and the inner content is returned verbatim. TypeScript code blocks are
// accepted but type annotations will likely cause a parse error at execution
// time — that's a feedback signal for the next LLM call, not a bug here.
func ExtractCode(message string) string {
	m := codeBlockRE.FindStringSubmatch(message)
	if m == nil {
		return ""
	}
	lang := strings.ToLower(strings.TrimSpace(m[1]))
	body := strings.TrimSpace(m[2])
	// If the block has no language tag, only accept it if it really looks
	// like JS — otherwise we might consume Python/SQL/markdown samples.
	if lang == "" {
		if !looksLikeJS(body) {
			return ""
		}
	}
	return body
}

func looksLikeJS(body string) bool {
	hints := []string{"client.", "return ", "const ", "let ", "function "}
	for _, h := range hints {
		if strings.Contains(body, h) {
			return true
		}
	}
	return false
}
