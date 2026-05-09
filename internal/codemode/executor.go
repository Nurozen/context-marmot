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
}

// Executor wraps a graph engine in a goja sandbox. Each call to Execute
// constructs a fresh runtime — there is no state shared between executions.
type Executor struct {
	engine  *mcpserver.Engine
	timeout time.Duration
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

// Execute runs jsCode in a fresh sandbox. The code is implicitly wrapped in
// an IIFE so a top-level `return` works. The returned *Result is never nil;
// timeouts, parse errors, and runtime exceptions all populate Result.Error.
func (e *Executor) Execute(ctx context.Context, jsCode string) *Result {
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

	// Register `client` proxy. Nil engine is permitted for sandbox-only
	// scenarios (tests, dry-run prompts) — `client` is simply absent.
	if e.engine != nil {
		if err := registerClient(rt, e.engine); err != nil {
			res.Error = "failed to wire client API: " + err.Error()
			res.DurationMS = time.Since(start).Milliseconds()
			return res
		}
	}

	// Wrap user code in an IIFE so `return` works at top level.
	wrapped := "(function(){\n" + jsCode + "\n})()"

	// Set up timeout via goja Interrupt + cancellable goroutine.
	timeout := e.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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
