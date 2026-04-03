package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// buildReport aggregates results into a full EvalReport.
func buildReport(results []EvalResult, model string) EvalReport {
	n := float64(len(results))
	if n == 0 {
		return EvalReport{Model: model, Timestamp: time.Now().UTC().Format(time.RFC3339)}
	}

	var vs, ms, hs ConditionSummary
	for _, r := range results {
		vs.AvgQuality += r.JudgeVanilla.Average()
		vs.AvgTurns += float64(r.Vanilla.Turns)
		vs.AvgTokens += float64(r.Vanilla.TokensIn + r.Vanilla.TokensOut)
		vs.AvgCost += r.Vanilla.CostUSD
		vs.AvgDuration += float64(r.Vanilla.DurationMS)

		ms.AvgQuality += r.JudgeMCP.Average()
		ms.AvgTurns += float64(r.MCP.Turns)
		ms.AvgTokens += float64(r.MCP.TokensIn + r.MCP.TokensOut)
		ms.AvgCost += r.MCP.CostUSD
		ms.AvgDuration += float64(r.MCP.DurationMS)

		hs.AvgQuality += r.JudgeHybrid.Average()
		hs.AvgTurns += float64(r.Hybrid.Turns)
		hs.AvgTokens += float64(r.Hybrid.TokensIn + r.Hybrid.TokensOut)
		hs.AvgCost += r.Hybrid.CostUSD
		hs.AvgDuration += float64(r.Hybrid.DurationMS)
	}
	vs.AvgQuality /= n
	vs.AvgTurns /= n
	vs.AvgTokens /= n
	vs.AvgCost /= n
	vs.AvgDuration /= n
	ms.AvgQuality /= n
	ms.AvgTurns /= n
	ms.AvgTokens /= n
	ms.AvgCost /= n
	ms.AvgDuration /= n
	hs.AvgQuality /= n
	hs.AvgTurns /= n
	hs.AvgTokens /= n
	hs.AvgCost /= n
	hs.AvgDuration /= n

	return EvalReport{
		Model:     model,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Results:   results,
		Summary:   ReportSummary{Vanilla: vs, MCP: ms, Hybrid: hs},
	}
}

// writeJSON writes the report to a JSON file.
func writeJSON(report EvalReport, path string) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// writeMarkdown writes a human-readable summary.
func writeMarkdown(report EvalReport, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	vs := report.Summary.Vanilla
	ms := report.Summary.MCP

	fmt.Fprintf(f, "# SWE-QA A/B Evaluation Results\n\n")
	fmt.Fprintf(f, "- **Model:** %s\n", report.Model)
	fmt.Fprintf(f, "- **Timestamp:** %s\n", report.Timestamp)
	fmt.Fprintf(f, "- **Questions:** %d\n\n", len(report.Results))

	hs := report.Summary.Hybrid

	fmt.Fprintf(f, "## Summary\n\n")
	fmt.Fprintf(f, "| Metric | Vanilla (file tools) | MCP only | Hybrid (MCP + files) |\n")
	fmt.Fprintf(f, "|--------|---------------------|----------|---------------------|\n")
	fmt.Fprintf(f, "| Avg quality (1-5) | %.2f | %.2f | %.2f |\n", vs.AvgQuality, ms.AvgQuality, hs.AvgQuality)
	fmt.Fprintf(f, "| Avg turns | %.1f | %.1f | %.1f |\n", vs.AvgTurns, ms.AvgTurns, hs.AvgTurns)
	fmt.Fprintf(f, "| Avg tokens | %.0f | %.0f | %.0f |\n", vs.AvgTokens, ms.AvgTokens, hs.AvgTokens)
	fmt.Fprintf(f, "| Avg cost | $%.4f | $%.4f | $%.4f |\n", vs.AvgCost, ms.AvgCost, hs.AvgCost)
	fmt.Fprintf(f, "| Avg duration | %.0fms | %.0fms | %.0fms |\n\n", vs.AvgDuration, ms.AvgDuration, hs.AvgDuration)

	fmt.Fprintf(f, "## Per-Question Results\n\n")
	fmt.Fprintf(f, "| ID | V Quality | M Quality | H Quality | V Turns | M Turns | H Turns | V Cost | M Cost | H Cost |\n")
	fmt.Fprintf(f, "|----|-----------|-----------|-----------|---------|---------|---------|--------|--------|--------|\n")
	for _, r := range report.Results {
		fmt.Fprintf(f, "| %s | %.1f | %.1f | %.1f | %d | %d | %d | $%.4f | $%.4f | $%.4f |\n",
			r.QuestionID,
			r.JudgeVanilla.Average(), r.JudgeMCP.Average(), r.JudgeHybrid.Average(),
			r.Vanilla.Turns, r.MCP.Turns, r.Hybrid.Turns,
			r.Vanilla.CostUSD, r.MCP.CostUSD, r.Hybrid.CostUSD)
	}

	fmt.Fprintf(f, "\n## Judge Rationales\n\n")
	for _, r := range report.Results {
		fmt.Fprintf(f, "### %s\n\n", r.QuestionID)
		fmt.Fprintf(f, "**Question:** %s\n\n", truncate(r.Question, 200))
		fmt.Fprintf(f, "**Vanilla** (C:%d Co:%d S:%d = %.1f): %s\n\n",
			r.JudgeVanilla.Correctness, r.JudgeVanilla.Completeness, r.JudgeVanilla.Specificity,
			r.JudgeVanilla.Average(), r.JudgeVanilla.Rationale)
		fmt.Fprintf(f, "**MCP** (C:%d Co:%d S:%d = %.1f): %s\n\n",
			r.JudgeMCP.Correctness, r.JudgeMCP.Completeness, r.JudgeMCP.Specificity,
			r.JudgeMCP.Average(), r.JudgeMCP.Rationale)
		fmt.Fprintf(f, "**Hybrid** (C:%d Co:%d S:%d = %.1f): %s\n\n",
			r.JudgeHybrid.Correctness, r.JudgeHybrid.Completeness, r.JudgeHybrid.Specificity,
			r.JudgeHybrid.Average(), r.JudgeHybrid.Rationale)
	}

	return nil
}

// printSummary prints the summary to stdout.
func printSummary(report EvalReport) {
	vs := report.Summary.Vanilla
	ms := report.Summary.MCP

	fmt.Println()
	hs := report.Summary.Hybrid

	fmt.Println("╔═══════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║             SWE-QA A/B/C Eval: Vanilla vs MCP vs Hybrid                 ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  Model: %-65s ║\n", report.Model)
	fmt.Printf("║  Questions: %-61d ║\n", len(report.Results))
	fmt.Println("╠═══════════════════════════════════════════════════════════════════════════╣")
	fmt.Println("║                          Vanilla      MCP only     Hybrid (MCP+files)   ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  Avg quality (1-5):       %-13.2f%-13.2f%-13.2f║\n", vs.AvgQuality, ms.AvgQuality, hs.AvgQuality)
	fmt.Printf("║  Avg turns:               %-13.1f%-13.1f%-13.1f║\n", vs.AvgTurns, ms.AvgTurns, hs.AvgTurns)
	fmt.Printf("║  Avg tokens:              %-13.0f%-13.0f%-13.0f║\n", vs.AvgTokens, ms.AvgTokens, hs.AvgTokens)
	fmt.Printf("║  Avg cost:                $%-12.4f$%-12.4f$%-12.4f║\n", vs.AvgCost, ms.AvgCost, hs.AvgCost)
	fmt.Printf("║  Avg duration:            %-10.0f ms%-10.0f ms%-10.0f ms║\n", vs.AvgDuration, ms.AvgDuration, hs.AvgDuration)
	fmt.Println("╚═══════════════════════════════════════════════════════════════════════════╝")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
