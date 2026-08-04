package main

import (
	"fmt"
	"regexp"
)

// riskRule pairs a pattern with a human explanation of why it is risky.
//
// Go's regexp engine is RE2 and has no lookaround, so exceptions are expressed
// with a second "unless" pattern rather than a negative lookahead.
type riskRule struct {
	re     *regexp.Regexp
	unless *regexp.Regexp
	reason string
}

func (r riskRule) matches(cmd string) bool {
	if !r.re.MatchString(cmd) {
		return false
	}
	if r.unless != nil && r.unless.MatchString(cmd) {
		return false
	}
	return true
}

// builtinRisks are patterns that warrant more than a casual "y".
//
// This is a speed bump for the obvious catastrophes, not a sandbox. Shell is
// too expressive to filter reliably, which is exactly why the command is always
// shown before it runs.
var builtinRisks = []riskRule{
	{re: regexp.MustCompile(`\brm\b[^|;&]*\s-[a-zA-Z]*[rR][a-zA-Z]*f|\brm\b[^|;&]*\s-[a-zA-Z]*f[a-zA-Z]*[rR]`), reason: "recursive force delete (rm -rf)"},
	{re: regexp.MustCompile(`\brm\b[^|;&]*\s+(/|~|\$HOME|\*)\s*$`), reason: "deletes a top-level or home path"},
	{re: regexp.MustCompile(`\bmkfs(\.\w+)?\b`), reason: "formats a filesystem"},
	{re: regexp.MustCompile(`\bdd\b[^|;&]*\bof=/dev/`), reason: "writes raw bytes to a device"},
	{re: regexp.MustCompile(`>\s*/dev/(sd|nvme|disk|hd)`), reason: "overwrites a raw disk device"},
	{re: regexp.MustCompile(`:\s*\(\s*\)\s*\{.*\}\s*;?\s*:`), reason: "fork bomb"},
	{re: regexp.MustCompile(`\bchmod\b[^|;&]*\s-[a-zA-Z]*R[a-zA-Z]*\s[^|;&]*777`), reason: "recursive world-writable permissions"},
	{re: regexp.MustCompile(`\bchmod\b[^|;&]*\s+777\s+/\s*$`), reason: "world-writable root"},
	{re: regexp.MustCompile(`\bchown\b[^|;&]*\s-[a-zA-Z]*R[a-zA-Z]*\s[^|;&]*\s/\s*$`), reason: "recursive ownership change on /"},
	{re: regexp.MustCompile(`\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba|z|k)?sh\b`), reason: "pipes a downloaded script straight into a shell"},
	{
		re:     regexp.MustCompile(`\bgit\b[^|;&]*\bpush\b[^|;&]*(--force|\s-f\b)`),
		unless: regexp.MustCompile(`--force-with-lease`),
		reason: "force-pushes and can destroy remote history",
	},
	{re: regexp.MustCompile(`\bgit\b[^|;&]*\breset\b[^|;&]*\s--hard\b`), reason: "discards uncommitted work"},
	{re: regexp.MustCompile(`\bgit\b[^|;&]*\bclean\b[^|;&]*\s-[a-zA-Z]*f`), reason: "deletes untracked files"},
	{re: regexp.MustCompile(`(?i)\bdrop\s+(database|table)\b`), reason: "drops a database or table"},
	{
		re:     regexp.MustCompile(`(?i)\b(truncate\s+table|delete\s+from)\b`),
		unless: regexp.MustCompile(`(?i)\bwhere\b`),
		reason: "SQL delete with no WHERE clause",
	},
	{re: regexp.MustCompile(`\b(shutdown|reboot|halt|poweroff)\b`), reason: "shuts down or reboots the machine"},
	{re: regexp.MustCompile(`\bkillall\b|\bkill\s+-9\s+-1\b|\bpkill\b[^|;&]*\s-9`), reason: "force-kills processes"},
	{re: regexp.MustCompile(`\bhistory\s+-c\b|>\s*~?/?\.(bash|zsh)_history`), reason: "erases shell history"},
	{re: regexp.MustCompile(`\bsudo\b`), reason: "runs with root privileges"},
	{re: regexp.MustCompile(`\b(shred|srm)\b`), reason: "irreversibly destroys file contents"},
	{re: regexp.MustCompile(`\bxargs\b[^|;&]*\brm\b`), reason: "deletes files in bulk via xargs"},
	{re: regexp.MustCompile(`-exec\s+rm\b`), reason: "deletes files found by find"},
	{re: regexp.MustCompile(`\bdocker\b[^|;&]*\b(system\s+prune|rm\s+-f|volume\s+rm)\b`), reason: "removes docker resources"},
	{re: regexp.MustCompile(`\bkubectl\b[^|;&]*\bdelete\b`), reason: "deletes Kubernetes resources"},
	{re: regexp.MustCompile(`\bterraform\b[^|;&]*\bdestroy\b`), reason: "destroys infrastructure"},
	{re: regexp.MustCompile(`\baws\b[^|;&]*\b(delete|terminate)-`), reason: "deletes AWS resources"},
}

// Risks returns the reasons a command is considered dangerous. An empty result
// means only a normal y/N confirmation is needed.
func Risks(cmd string, extraPatterns []string) []string {
	var reasons []string
	seen := map[string]bool{}

	add := func(reason string) {
		if !seen[reason] {
			seen[reason] = true
			reasons = append(reasons, reason)
		}
	}

	for _, rule := range builtinRisks {
		if rule.matches(cmd) {
			add(rule.reason)
		}
	}
	for _, p := range extraPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue // a bad user pattern must not break the tool
		}
		if re.MatchString(cmd) {
			add(fmt.Sprintf("matches your dangerous_patterns rule %q", p))
		}
	}
	return reasons
}
