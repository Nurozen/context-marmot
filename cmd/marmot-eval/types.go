package main

// EvalQuestion is a single SWE-QA question for evaluation.
type EvalQuestion struct {
	ID              string   `json:"id"`
	Repo            string   `json:"repo"`
	Question        string   `json:"question"`
	Answer          string   `json:"answer"`
	ReferencedFiles []string `json:"referenced_files"`
}

// RepoConfig defines a repository to clone for evaluation.
type RepoConfig struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Ref    string `json:"ref"`
	Subdir string `json:"subdir"`
}

// RunResult holds the output from a single claude CLI invocation.
type RunResult struct {
	Answer    string  `json:"answer"`
	Turns     int     `json:"turns"`
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	CostUSD   float64 `json:"cost_usd"`
	DurationMS int    `json:"duration_ms"`
}

// JudgeScore holds the judge's rubric scores for an answer.
type JudgeScore struct {
	Correctness  int    `json:"correctness"`
	Completeness int    `json:"completeness"`
	Specificity  int    `json:"specificity"`
	Rationale    string `json:"rationale"`
}

// Average returns the mean of the three dimension scores.
func (j JudgeScore) Average() float64 {
	return float64(j.Correctness+j.Completeness+j.Specificity) / 3.0
}

// EvalResult bundles all conditions and judge scores for one question.
type EvalResult struct {
	QuestionID   string     `json:"question_id"`
	Question     string     `json:"question"`
	Vanilla      RunResult  `json:"vanilla"`
	MCP          RunResult  `json:"mcp"`
	Hybrid       RunResult  `json:"hybrid"`
	JudgeVanilla JudgeScore `json:"judge_vanilla"`
	JudgeMCP     JudgeScore `json:"judge_mcp"`
	JudgeHybrid  JudgeScore `json:"judge_hybrid"`
}

// EvalReport is the top-level output.
type EvalReport struct {
	Model     string        `json:"model"`
	Timestamp string        `json:"timestamp"`
	Results   []EvalResult  `json:"results"`
	Summary   ReportSummary `json:"summary"`
}

// ConditionSummary holds aggregated metrics for one condition.
type ConditionSummary struct {
	AvgQuality float64 `json:"avg_quality"`
	AvgTurns   float64 `json:"avg_turns"`
	AvgTokens  float64 `json:"avg_tokens"`
	AvgCost    float64 `json:"avg_cost"`
	AvgDuration float64 `json:"avg_duration_ms"`
}

// ReportSummary holds all condition summaries.
type ReportSummary struct {
	Vanilla ConditionSummary `json:"vanilla"`
	MCP     ConditionSummary `json:"mcp"`
	Hybrid  ConditionSummary `json:"hybrid"`
}

// ClaudeJSONOutput is the JSON structure returned by `claude -p --output-format json`.
type ClaudeJSONOutput struct {
	Type       string  `json:"type"`
	Result     string  `json:"result"`
	NumTurns   int     `json:"num_turns"`
	CostUSD    float64 `json:"total_cost_usd"`
	DurationMS int     `json:"duration_ms"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		CacheCreation struct {
			Ephemeral1H int `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
		CacheRead int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// ClaudeStructuredOutput wraps the judge's structured JSON response.
type ClaudeStructuredOutput struct {
	Type             string     `json:"type"`
	Result           string     `json:"result"`
	StructuredOutput JudgeScore `json:"structured_output"`
	CostUSD          float64    `json:"total_cost_usd"`
}
