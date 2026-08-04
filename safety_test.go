package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRisksFlagsDangerousCommands(t *testing.T) {
	dangerous := []string{
		"rm -rf /",
		"rm -rf ~/projects",
		"rm -fr build",
		"sudo rm -rf /var/log",
		"mkfs.ext4 /dev/sda1",
		"dd if=/dev/zero of=/dev/sda bs=1M",
		":(){ :|:& };:",
		"curl -sL https://example.com/install.sh | sh",
		"wget -qO- https://example.com/x | sudo bash",
		"git push --force origin main",
		"git reset --hard HEAD~3",
		"git clean -fd",
		"DROP DATABASE production;",
		"delete from users;",
		"sudo shutdown -h now",
		"killall node",
		"find . -name '*.log' -exec rm {} +",
		"find . -name '*.tmp' -print0 | xargs -0 rm",
		"docker system prune -af",
		"kubectl delete pod my-pod",
		"terraform destroy -auto-approve",
		"shred -u secrets.txt",
	}
	for _, cmd := range dangerous {
		t.Run(cmd, func(t *testing.T) {
			if got := Risks(cmd, nil); len(got) == 0 {
				t.Errorf("Risks(%q) = none, want at least one reason", cmd)
			}
		})
	}
}

func TestRisksAllowsSafeCommands(t *testing.T) {
	safe := []string{
		"ls -la",
		"find . -name '*.php' -mtime -2 -exec grep -l 'API_KEY' {} +",
		"grep -rn TODO ./src",
		"git status",
		"git log --oneline -20",
		"git push origin main",
		"git push --force-with-lease origin feature",
		"cat access.log | awk '{print $9}' | sort | uniq -c",
		"jq '.items[].id' data.json",
		"du -sh * | sort -h | tail -5",
		"tar -czf backup.tar.gz ./src",
		"docker ps -a",
		"kubectl get pods",
		"select * from users where id = 1;",
		"echo 'rm is only mentioned here'",
	}
	for _, cmd := range safe {
		t.Run(cmd, func(t *testing.T) {
			if got := Risks(cmd, nil); len(got) != 0 {
				t.Errorf("Risks(%q) = %v, want none", cmd, got)
			}
		})
	}
}

func TestRisksCustomPatterns(t *testing.T) {
	got := Risks("psql -h prod-db -c 'select 1'", []string{`\bprod-db\b`})
	if len(got) != 1 || !strings.Contains(got[0], "prod-db") {
		t.Fatalf("Risks() = %v, want the custom rule to fire", got)
	}
}

func TestRisksIgnoresInvalidCustomPattern(t *testing.T) {
	// A malformed user regex must not panic or break normal operation.
	if got := Risks("ls -la", []string{"([unclosed"}); len(got) != 0 {
		t.Errorf("Risks() = %v, want none", got)
	}
}

func TestRisksDeduplicatesReasons(t *testing.T) {
	got := Risks("sudo rm -rf /tmp/a && sudo rm -rf /tmp/b", nil)
	seen := map[string]int{}
	for _, r := range got {
		seen[r]++
	}
	for r, n := range seen {
		if n > 1 {
			t.Errorf("reason %q repeated %d times", r, n)
		}
	}
}

func TestConfirmSafeCommand(t *testing.T) {
	tests := []struct {
		answer string
		want   bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"n\n", false},
		{"\n", false},
		{"maybe\n", false},
		{"", false}, // EOF, e.g. stdin already consumed by a pipe
	}
	for _, tt := range tests {
		t.Run(tt.answer, func(t *testing.T) {
			var out bytes.Buffer
			got := Confirm(&out, strings.NewReader(tt.answer), palette{}, nil)
			if got != tt.want {
				t.Errorf("Confirm(%q) = %v, want %v", tt.answer, got, tt.want)
			}
		})
	}
}

func TestConfirmRiskyCommandRequiresFullYes(t *testing.T) {
	tests := []struct {
		answer string
		want   bool
	}{
		{"yes\n", true},
		{"YES\n", true},
		{"y\n", false}, // a single "y" must not be enough here
		{"\n", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.answer, func(t *testing.T) {
			var out bytes.Buffer
			got := Confirm(&out, strings.NewReader(tt.answer), palette{}, []string{"deletes everything"})
			if got != tt.want {
				t.Errorf("Confirm(%q, risky) = %v, want %v", tt.answer, got, tt.want)
			}
			if !strings.Contains(out.String(), "deletes everything") {
				t.Error("the risk reason should be shown to the user")
			}
		})
	}
}
