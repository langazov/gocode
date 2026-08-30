package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"

	"github.com/anomalyco/opencode-go/internal/clix"
)

// prCommand mirrors PrCommand in cli/cmd/pr.ts ("pr <number>"): shells out to
// the gh CLI to fetch and check out a PR branch, then re-execs opencode.
func prCommand() *clix.Command {
	return &clix.Command{
		Name:        "pr",
		Describe:    "fetch and checkout a GitHub PR branch, then run opencode",
		Positionals: []clix.Positional{{Name: "number", Required: true, Describe: "PR number to checkout"}},
		Run:         runPR,
	}
}

var sessionLinkRe = regexp.MustCompile(`https://opncd\.ai/s/([a-zA-Z0-9_-]+)`)

func runPR(a *clix.Args) error {
	prNumber, err := strconv.Atoi(a.Pos["number"])
	if err != nil {
		return &usageError{msg: "PR number must be an integer"}
	}
	localBranch := fmt.Sprintf("pr/%d", prNumber)
	fmt.Printf("Fetching and checking out PR #%d...\n", prNumber)

	checkout := exec.Command("gh", "pr", "checkout", strconv.Itoa(prNumber), "--branch", localBranch, "--force")
	checkout.Stdout, checkout.Stderr = os.Stdout, os.Stderr
	if err := checkout.Run(); err != nil {
		return fmt.Errorf("failed to checkout PR #%d: make sure gh CLI is installed and authenticated", prNumber)
	}

	view := exec.Command("gh", "pr", "view", strconv.Itoa(prNumber), "--json", "headRepository,headRepositoryOwner,isCrossRepository,headRefName,body")
	output, _ := view.Output()
	var sessionID string
	if len(output) > 0 {
		var info struct {
			Body string `json:"body"`
		}
		if json.Unmarshal(output, &info) == nil {
			if m := sessionLinkRe.FindStringSubmatch(info.Body); m != nil {
				fmt.Printf("Found opencode session: %s\n", m[0])
				fmt.Println("Importing session...")
				imp := exec.Command("opencode", "import", m[0])
				imp.Stdout, imp.Stderr = os.Stdout, os.Stderr
				if imp.Run() == nil {
					sessionID = ""
				}
			}
		}
	}

	fmt.Printf("Successfully checked out PR #%d as branch '%s'\n\n", prNumber, localBranch)
	fmt.Println("Starting opencode...")
	fmt.Println()

	args := []string{}
	if sessionID != "" {
		args = append(args, "-s", sessionID)
	}
	run := exec.Command("opencode", args...)
	run.Stdin, run.Stdout, run.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := run.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("opencode exited with code %d", exitErr.ExitCode())
		}
		return err
	}
	return nil
}
