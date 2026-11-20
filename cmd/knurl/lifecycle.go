package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ViniTamanhao/knurl/internal/approval"
	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/registry"
	"github.com/ViniTamanhao/knurl/internal/runtime"
)

type lifecycleOptions struct {
	target, reason, status string
	last, yes, json        bool
	scope                  contract.Scope
	to                     int
	help                   bool
}

func parseLifecycle(command string, args []string) (lifecycleOptions, error) {
	o := lifecycleOptions{status: "pending", scope: contract.ScopeAuto}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--help" || a == "-h" {
			o.help = true
			return o, nil
		}
		flag, value, inline := strings.Cut(a, "=")
		inline = inline && strings.HasPrefix(a, "--")
		if inline && flag != "--reason" && flag != "--status" && flag != "--scope" && flag != "--to" {
			return o, fmt.Errorf("unknown flag %q", a)
		}
		if inline {
			a = flag
		}
		switch {
		case a == "--last" && (command == "approve" || command == "reject"):
			o.last = true
		case (a == "--yes" || a == "-y") && command == "approve":
			o.yes = true
		case a == "--json" && command == "proposals":
			o.json = true
		case (a == "--reason" && command == "reject") || (a == "--status" && command == "proposals") || (a == "--scope" && command == "rollback") || (a == "--to" && command == "rollback"):
			v := value
			if !inline {
				var next int
				var err error
				v, next, err = nextFlagValue(args, i, a)
				if err != nil {
					return o, err
				}
				i = next
			}
			switch a {
			case "--reason":
				o.reason = v
			case "--status":
				o.status = v
			case "--scope":
				if v != "repo" && v != "global" {
					return o, fmt.Errorf("scope must be repo or global")
				}
				o.scope = contract.Scope(v)
			case "--to":
				n, e := parseVersionFlag(v)
				if e != nil {
					return o, e
				}
				o.to = n
			}
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown flag %q", a)
		default:
			if o.target != "" || command == "proposals" {
				return o, fmt.Errorf("unexpected argument %q", a)
			}
			o.target = a
		}
	}
	if o.status != "pending" && o.status != "all" {
		return o, fmt.Errorf("status must be pending or all")
	}
	if o.last && o.target != "" {
		return o, fmt.Errorf("use a proposal ID or --last")
	}
	if command != "proposals" && o.target == "" && !o.last {
		return o, fmt.Errorf("missing target (use --last for a proposal)")
	}
	return o, nil
}

func runLifecycleCmd(command string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := parseLifecycle(command, args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "knurl %s: %v\n", command, err)
		return 2
	}
	if opts.help {
		return writeOut(stdout, lifecycleHelp(command), stderr)
	}
	workDir, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	reg := &registry.Registry{WorkDir: workDir}
	store := &approval.Store{Registry: reg, Supervisor: &runtime.Supervisor{}, WorkDir: workDir}
	fail := func(e error) int { _, _ = fmt.Fprintf(stderr, "knurl %s: %v\n", command, e); return 1 }
	if opts.last {
		opts.target, err = store.Last()
		if err != nil {
			return fail(err)
		}
	}
	switch command {
	case "proposals":
		list, e := store.List(opts.status == "all")
		if e != nil {
			return fail(e)
		}
		if opts.json {
			type row struct {
				ID      string                  `json:"proposal_id"`
				Tool    string                  `json:"tool"`
				Kind    string                  `json:"kind"`
				Scope   contract.Scope          `json:"scope"`
				Status  contract.ProposalStatus `json:"status"`
				Created string                  `json:"created_at"`
			}
			rows := []row{}
			for _, p := range list {
				rows = append(rows, row{p.ProposalID, p.ToolName, p.Kind, p.Scope, p.Status, p.CreatedAt.Format("2006-01-02T15:04:05Z")})
			}
			return writeJSONLine(stdout, rows, stderr)
		}
		for _, p := range list {
			_, _ = fmt.Fprintf(stdout, "%s  %s  %s  %s  %s  %s\n", p.ProposalID, p.ToolName, p.Kind, p.Scope, p.Status, p.CreatedAt.Format("2006-01-02T15:04:05Z"))
		}
		return 0
	case "reject":
		p, e := store.Reject(opts.target, opts.reason)
		if e != nil {
			return fail(e)
		}
		_, _ = fmt.Fprintf(stdout, "Rejected %s (%s)\n", p.ProposalID, p.ToolName)
		return 0
	case "rollback":
		before, after, e := reg.Rollback(opts.target, opts.to, opts.scope)
		if e != nil {
			return fail(e)
		}
		_, _ = fmt.Fprintf(stdout, "Rolled back %s: v%d -> v%d\n", opts.target, before, after)
		return 0
	case "approve":
		p, files, e := store.Review(opts.target)
		if e != nil {
			return fail(e)
		}
		if e = renderReview(stdout, p, files); e != nil {
			return fail(e)
		}
		if !opts.yes {
			if _, e = io.WriteString(stdout, "Approve? [y/N] "); e != nil {
				return fail(e)
			}
			line, readErr := bufio.NewReader(stdin).ReadString('\n')
			if readErr != nil && readErr != io.EOF {
				return fail(readErr)
			}
			answer := strings.ToLower(strings.TrimSpace(line))
			if answer != "y" && answer != "yes" {
				_, _ = fmt.Fprintln(stdout, "Approval cancelled; proposal remains pending.")
				return 0
			}
		}
		approved, e := store.Approve(p.ProposalID, p.BundleSHA256)
		if e != nil {
			return fail(e)
		}
		_, _ = fmt.Fprintf(stdout, "Approved %s: %s v%d (%s)\n", approved.ProposalID, approved.ToolName, approved.PromotedVersion, approved.Scope)
		return 0
	}
	return 2
}

func renderReview(w io.Writer, p contract.ProposalRecord, files map[string][]byte) error {
	var m contract.Manifest
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		return err
	}
	var b strings.Builder
	action := "create v" + strconv.Itoa(p.TargetVersion)
	if p.BaseVersion > 0 {
		action = fmt.Sprintf("repair v%d -> v%d", p.BaseVersion, p.TargetVersion)
	}
	target := string(p.Scope)
	if p.TargetRepoRoot != "" {
		target += " " + p.TargetRepoRoot
	}
	fmt.Fprintf(&b, "Proposal %s\nTool: %s | %s\nTarget: %s\nSummary: %s\n", p.ProposalID, p.ToolName, action, target, strconv.Quote(p.Summary))
	if p.Elevated {
		b.WriteString("*** ELEVATED PERMISSIONS ***\n")
	}
	if p.CapabilityDiff.HasEscalation {
		b.WriteString("*** NEW / ESCALATED PERMISSIONS ***\n")
	}
	b.WriteString("Capabilities:\n")
	for _, c := range p.CapabilityDiff.Changes {
		fmt.Fprintf(&b, "  %s %s: %s\n", strings.ToUpper(c.Kind), c.Capability, c.Detail)
	}
	for _, s := range m.Secrets {
		fmt.Fprintf(&b, "Secret: %s (required=%t)\n", s.Name, s.Required)
	}
	fmt.Fprintf(&b, "Fixtures: %d/%d passed\n", p.Validation.TestsPassed, p.Validation.TestsTotal)
	if p.DryRun != nil {
		if !p.DryRun.Executed {
			fmt.Fprintf(&b, "Live preview skipped: %s\n", p.DryRun.SkippedReason)
		} else {
			data, _ := json.MarshalIndent(p.DryRun, "", "  ")
			fmt.Fprintf(&b, "Live preview: %s\n", data)
		}
	}
	if p.BaseVersion > 0 {
		fmt.Fprintf(&b, "Code diff:\n%s\n", p.SourceDiff)
	} else {
		source := string(files["tool.js"])
		if len(source) > 12000 {
			end := 12000
			for !utf8.RuneStart(source[end]) {
				end--
			}
			source = source[:end] + "\n[review truncated; inspect staged tool.js before approving]"
		}
		fmt.Fprintf(&b, "Source:\n%s\n", source)
	}
	// Quote terminal control bytes so untrusted source cannot hide permissions.
	text := strings.Map(func(r rune) rune {
		if (r < 32 && r != '\n' && r != '\t') || r == 127 || r == 0x202e {
			return '�'
		}
		return r
	}, b.String())
	_, err := io.WriteString(w, text)
	return err
}

func lifecycleHelp(command string) string {
	usage := map[string]string{"approve": "[<proposal-id> | --last] [--yes | -y]\n  --last       Select newest pending proposal\n  --yes, -y    Approve without prompting", "reject": "[<proposal-id> | --last] [--reason <text>]\n  --last       Select newest pending proposal\n  --reason     Record the human rejection reason", "rollback": "<tool> [--to v<N>] [--scope repo|global]\n  --to         Restore an installed approved version\n  --scope      Select a registry tier", "proposals": "[--status pending|all] [--json]\n  --status     Filter proposals (default pending)\n  --json       Print compact JSON rows"}
	return "Usage: knurl " + command + " " + usage[command] + "\n  -h, --help   Show command help\n"
}
