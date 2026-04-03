package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	os.Exit(run())
}

func run() int {
	fs := flag.NewFlagSet("marmot-eval", flag.ContinueOnError)
	model := fs.String("model", "sonnet", "Claude model to use")
	workDir := fs.String("work-dir", "", "Working directory for clones and vaults (default: temp dir)")
	maxQuestions := fs.Int("questions", 0, "Max questions to evaluate (0 = all)")
	outputDir := fs.String("output", ".", "Directory for result files")
	skipVanilla := fs.Bool("skip-vanilla", false, "Skip vanilla condition (debug)")
	skipMCP := fs.Bool("skip-mcp", false, "Skip MCP condition (debug)")
	skipHybrid := fs.Bool("skip-hybrid", false, "Skip hybrid condition (debug)")
	skipJudge := fs.Bool("skip-judge", false, "Skip judge scoring (debug)")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return 1
	}

	// Resolve paths.
	repoRoot := findRepoRoot()
	questionsPath := filepath.Join(repoRoot, "testdata", "eval", "questions.json")
	reposPath := filepath.Join(repoRoot, "testdata", "eval", "repos.json")
	marmotBinary := filepath.Join(repoRoot, "bin", "marmot")

	// Load questions.
	questions, err := loadQuestions(questionsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load questions: %v\n", err)
		return 1
	}
	if *maxQuestions > 0 && *maxQuestions < len(questions) {
		questions = questions[:*maxQuestions]
	}

	// Load repos.
	repos, err := loadRepos(reposPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load repos: %v\n", err)
		return 1
	}

	// Set up work directory.
	if *workDir == "" {
		d, err := os.MkdirTemp("", "marmot-eval-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "create work dir: %v\n", err)
			return 1
		}
		*workDir = d
		fmt.Printf("Work directory: %s\n", *workDir)
	}

	// Verify marmot binary.
	if _, err := os.Stat(marmotBinary); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "marmot binary not found at %s; run 'make build' first\n", marmotBinary)
		return 1
	}

	// Verify claude CLI.
	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintf(os.Stderr, "claude CLI not found in PATH\n")
		return 1
	}

	// --- Phase 0: Setup ---
	fmt.Println("\n=== Phase 0: Setup ===")

	// Clone repos.
	repoDirs := map[string]string{}
	for _, repo := range repos {
		dir, err := cloneRepo(repo, *workDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "clone %s: %v\n", repo.Name, err)
			return 1
		}
		repoDirs[repo.Name] = dir
	}

	// Seed vaults for MCP condition.
	vaultDirs := map[string]string{}
	if !*skipMCP || !*skipHybrid {
		// Group questions by repo for seeding.
		byRepo := map[string][]EvalQuestion{}
		for _, q := range questions {
			byRepo[q.Repo] = append(byRepo[q.Repo], q)
		}
		for repo, qs := range byRepo {
			dir, err := seedVault(qs, repo, *workDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "seed %s: %v\n", repo, err)
				return 1
			}
			vaultDirs[repo] = dir
		}
	}

	// --- Load checkpoint ---
	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create output dir: %v\n", err)
		return 1
	}
	checkpointPath := filepath.Join(*outputDir, "checkpoint.jsonl")
	checkpoint := loadCheckpoint(checkpointPath)
	if len(checkpoint) > 0 {
		fmt.Printf("Loaded checkpoint with %d completed results\n", len(checkpoint))
	}

	// --- Phase 1+2: Execute and Judge per question (with checkpointing) ---
	fmt.Println("\n=== Phase 1+2: Execute & Judge ===")

	var results []EvalResult
	for i, q := range questions {
		// Check if already checkpointed.
		if prev, ok := checkpoint[q.ID]; ok {
			fmt.Printf("[%d/%d] %s: restored from checkpoint (V:%.1f M:%.1f)\n",
				i+1, len(questions), q.ID, prev.JudgeVanilla.Average(), prev.JudgeMCP.Average())
			results = append(results, prev)
			continue
		}

		fmt.Printf("\n[%d/%d] %s: %s\n", i+1, len(questions), q.ID, truncate(q.Question, 80))

		result := EvalResult{
			QuestionID: q.ID,
			Question:   q.Question,
		}

		// Condition A: Vanilla.
		if !*skipVanilla {
			fmt.Printf("  Running vanilla...")
			repoDir := repoDirs[q.Repo]
			vr, err := runVanilla(q.Question, repoDir, *model)
			if err != nil {
				fmt.Printf(" ERROR: %v\n", err)
				vr = RunResult{Answer: fmt.Sprintf("ERROR: %v", err)}
			} else {
				fmt.Printf(" done (%d turns, $%.4f)\n", vr.Turns, vr.CostUSD)
			}
			result.Vanilla = vr
		}

		// Condition B: MCP only.
		if !*skipMCP {
			fmt.Printf("  Running MCP...")
			vaultDir := vaultDirs[q.Repo]
			mr, err := runMCP(q.Question, marmotBinary, vaultDir, *model)
			if err != nil {
				fmt.Printf(" ERROR: %v\n", err)
				mr = RunResult{Answer: fmt.Sprintf("ERROR: %v", err)}
			} else {
				fmt.Printf(" done (%d turns, $%.4f)\n", mr.Turns, mr.CostUSD)
			}
			result.MCP = mr
		}

		// Condition C: Hybrid (MCP + file tools).
		if !*skipHybrid {
			fmt.Printf("  Running hybrid...")
			repoDir := repoDirs[q.Repo]
			vaultDir := vaultDirs[q.Repo]
			hr, err := runHybrid(q.Question, repoDir, marmotBinary, vaultDir, *model)
			if err != nil {
				fmt.Printf(" ERROR: %v\n", err)
				hr = RunResult{Answer: fmt.Sprintf("ERROR: %v", err)}
			} else {
				fmt.Printf(" done (%d turns, $%.4f)\n", hr.Turns, hr.CostUSD)
			}
			result.Hybrid = hr
		}

		// Judge immediately.
		if !*skipJudge {
			fmt.Printf("  Judging...")
			if !*skipVanilla && result.Vanilla.Answer != "" {
				score, err := judgeAnswer(q.Question, q.Answer, result.Vanilla.Answer, *model)
				if err != nil {
					fmt.Printf(" vanilla judge error: %v", err)
				} else {
					result.JudgeVanilla = score
				}
			}
			if !*skipMCP && result.MCP.Answer != "" {
				score, err := judgeAnswer(q.Question, q.Answer, result.MCP.Answer, *model)
				if err != nil {
					fmt.Printf(" mcp judge error: %v", err)
				} else {
					result.JudgeMCP = score
				}
			}
			if !*skipHybrid && result.Hybrid.Answer != "" {
				score, err := judgeAnswer(q.Question, q.Answer, result.Hybrid.Answer, *model)
				if err != nil {
					fmt.Printf(" hybrid judge error: %v", err)
				} else {
					result.JudgeHybrid = score
				}
			}
			fmt.Printf(" done (V:%.1f M:%.1f H:%.1f)\n",
				result.JudgeVanilla.Average(), result.JudgeMCP.Average(), result.JudgeHybrid.Average())
		}

		results = append(results, result)

		// Checkpoint after each question.
		saveCheckpoint(checkpointPath, result)
	}

	// --- Phase 3: Reporting ---
	fmt.Println("\n=== Phase 3: Reporting ===")

	report := buildReport(results, *model)
	printSummary(report)

	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create output dir: %v\n", err)
		return 1
	}

	jsonPath := filepath.Join(*outputDir, "eval_results.json")
	if err := writeJSON(report, jsonPath); err != nil {
		fmt.Fprintf(os.Stderr, "write JSON: %v\n", err)
	} else {
		fmt.Printf("Results written to %s\n", jsonPath)
	}

	mdPath := filepath.Join(*outputDir, "eval_results.md")
	if err := writeMarkdown(report, mdPath); err != nil {
		fmt.Fprintf(os.Stderr, "write markdown: %v\n", err)
	} else {
		fmt.Printf("Report written to %s\n", mdPath)
	}

	return 0
}

func loadQuestions(path string) ([]EvalQuestion, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var qs []EvalQuestion
	return qs, json.Unmarshal(data, &qs)
}

func loadRepos(path string) ([]RepoConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var repos []RepoConfig
	return repos, json.Unmarshal(data, &repos)
}

// loadCheckpoint reads completed results from a JSONL checkpoint file.
func loadCheckpoint(path string) map[string]EvalResult {
	results := make(map[string]EvalResult)
	data, err := os.ReadFile(path)
	if err != nil {
		return results
	}
	for _, line := range splitLines(string(data)) {
		if line == "" {
			continue
		}
		var r EvalResult
		if err := json.Unmarshal([]byte(line), &r); err == nil && r.QuestionID != "" {
			results[r.QuestionID] = r
		}
	}
	return results
}

// saveCheckpoint appends a single result to the JSONL checkpoint file.
func saveCheckpoint(path string, result EvalResult) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: checkpoint write failed: %v\n", err)
		return
	}
	defer f.Close()
	data, _ := json.Marshal(result)
	f.Write(data)
	f.WriteString("\n")
}

func splitLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		lines = append(lines, strings.TrimSpace(line))
	}
	return lines
}

func findRepoRoot() string {
	// Walk up from executable or cwd to find go.mod.
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
