## internal/sdkgen
No relevant findings. The package (generate.go, generate_test.go) is a pure string generator that emits a TypeScript client SDK for the HTTP API; it opens no SQLite connections, builds no engine, and has no process/socket/lock/scheduler/stdio code, so neither workstream touches it.
