package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

const judgeSystemPrompt = `You are evaluating an answer to a code comprehension question. Score the candidate answer on three dimensions (1-5 each):

CORRECTNESS (1-5): Are the stated facts accurate? No hallucinated files, functions, or behaviors.
- 5: Every factual claim is verifiable in the codebase
- 3: Core facts are correct but some details are wrong or vague
- 1: Major factual errors or hallucinations

COMPLETENESS (1-5): Does the answer cover the key points from the gold answer?
- 5: Covers all gold-answer points and potentially adds valid insights
- 3: Gets the core concept but misses important details
- 1: Misses the main point entirely

SPECIFICITY (1-5): Does the answer reference concrete code?
- 5: Cites specific files, functions, line numbers, and code patterns
- 3: References some files/functions but lacks detail
- 1: Entirely conceptual with no code references

IMPORTANT: The gold answer is a REFERENCE, not the only correct answer. Do not penalize valid alternative explanations or additional insights not in the gold answer.`

const judgeSchema = `{
  "type": "object",
  "properties": {
    "correctness": {"type": "integer", "minimum": 1, "maximum": 5},
    "completeness": {"type": "integer", "minimum": 1, "maximum": 5},
    "specificity": {"type": "integer", "minimum": 1, "maximum": 5},
    "rationale": {"type": "string"}
  },
  "required": ["correctness", "completeness", "specificity", "rationale"]
}`

// judgeAnswer scores a candidate answer against the gold answer.
func judgeAnswer(question, goldAnswer, candidateAnswer, model string) (JudgeScore, error) {
	prompt := fmt.Sprintf(
		"QUESTION:\n%s\n\nGOLD ANSWER:\n%s\n\nCANDIDATE ANSWER:\n%s",
		question, goldAnswer, candidateAnswer)

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return JudgeScore{}, fmt.Errorf("claude CLI not found: %w", err)
	}

	args := []string{
		"-p",
		"--output-format", "json",
		"--model", model,
		"--dangerously-skip-permissions",
		"--no-session-persistence",
		"--tools", "",
		"--system-prompt", judgeSystemPrompt,
		"--json-schema", judgeSchema,
		"--max-budget-usd", "0.10",
	}

	cmd := exec.Command(claudePath, args...)
	cmd.Stdin = strings.NewReader(prompt)

	out, err := cmd.Output()
	if err != nil {
		if len(out) == 0 {
			return JudgeScore{}, fmt.Errorf("judge CLI failed: %w", err)
		}
	}

	// Try parsing structured_output first, fall back to result text.
	var structured ClaudeStructuredOutput
	if err := json.Unmarshal(out, &structured); err == nil && structured.StructuredOutput.Correctness > 0 {
		return structured.StructuredOutput, nil
	}

	// Fall back: parse the result text as JSON.
	var cOut ClaudeJSONOutput
	if err := json.Unmarshal(out, &cOut); err != nil {
		return JudgeScore{}, fmt.Errorf("parse judge output: %w", err)
	}

	var score JudgeScore
	if err := json.Unmarshal([]byte(cOut.Result), &score); err != nil {
		return JudgeScore{}, fmt.Errorf("parse judge score from result: %w\nresult: %s", err, cOut.Result[:min(len(cOut.Result), 500)])
	}
	return score, nil
}
