package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/open-e2ee/oe/internal/config"
	"github.com/open-e2ee/oe/internal/control"
	"github.com/open-e2ee/oe/internal/output"
	"github.com/open-e2ee/oe/internal/projectlock"
)

// relayFields are the fields of a Relay policy, in the order of the file.
var relayFields = []string{"deliveryRetention", "attachmentRetention"}

func (r *runner) config(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("config", "name a config command: pull")
	}
	switch args[0] {
	case "pull":
		return r.configPull(ctx, args[1:])
	default:
		return usageError("config", fmt.Sprintf("unknown config command %q", args[0]))
	}
}

// configPull writes the Relay policy of each active environment into
// open-e2ee.config.ts with the fewest changes. An addition needs no consent,
// and a replaced value needs --yes or a person's answer. When a changed value
// is computed in the file, the pull writes nothing and returns every edit
// that a person must make.
func (r *runner) configPull(ctx context.Context, args []string) error {
	flags := newFlags("config pull")
	yes := flags.Bool("yes", false, "replace values without a prompt")
	dryRun := flags.Bool("dry-run", false, "show the changes and write nothing")
	if err := parseFlags(flags, "config", args); err != nil {
		return err
	}
	path, value, err := r.loadConfig()
	if err != nil {
		return err
	}
	access, err := r.access(ctx, "project:read", false)
	if err != nil {
		return err
	}
	project, err := r.api.GetProject(ctx, control.CredentialRequest{AccessToken: access.AccessToken}, value.Project)
	if err != nil {
		return err
	}
	plan, err := planPull(value, project, r.shownEnvironments())
	if err != nil {
		return err
	}
	if len(plan.changes) == 0 {
		return r.out.Success("config pull", config.Filename+" matches the console.", plan.data("", false))
	}
	if err := config.CheckEdit(path, plan.changes...); err != nil {
		failure, ok := errors.AsType[*config.Error](err)
		if !*dryRun || !ok || failure.Code != "CONFIG_EDIT_REQUIRED" {
			return err
		}
		data := plan.data("planned", false)
		data["edits"] = failure.Data["edits"]
		return r.out.Success("config pull", plan.text("A pull needs these changes, and a person must make some of them:")+"\n"+failure.Message, data)
	}
	if *dryRun {
		return r.out.Success("config pull", plan.text("A pull makes these changes:"), plan.data("planned", false))
	}
	if plan.replaced > 0 && !*yes {
		next := "oe config pull --yes"
		if r.environmentSelected {
			next += " --env " + r.environment
		}
		if access.Source == "environment" || r.mode != output.Text || !r.canPrompt() {
			return &problem{
				code: "CONFIRMATION_REQUIRED", exit: exitUsage, next: next,
				message: fmt.Sprintf("a pull that replaces %d value(s) needs --yes when no person can answer a prompt; review the changes, then run %s", plan.replaced, next),
				data:    plan.data("planned", false),
			}
		}
		fmt.Fprintln(r.errOut, plan.text("A pull makes these changes:"))
		approved, err := askConfirmation(r.in, r.errOut, fmt.Sprintf("Replace %d value(s) in %s?", plan.replaced, config.Filename))
		if err != nil {
			return err
		}
		if !approved {
			return &problem{code: "PULL_CANCELLED", message: "config pull cancelled", exit: exitFailure}
		}
	}
	lock, err := projectlock.Acquire(ctx, filepath.Dir(path))
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := config.Edit(path, plan.changes...); err != nil {
		return err
	}
	return r.out.Success("config pull", plan.text("Pulled the Relay policy into "+config.Filename+":"), plan.data("applied", true))
}

// pullPlan is the changes that a pull makes to open-e2ee.config.ts.
type pullPlan struct {
	names        []string
	environments map[string]*pulledEnvironment
	changes      []config.Change
	// replaced counts the changes that replace a value that the file gives.
	replaced int
}

type pulledEnvironment struct {
	Status  string       `json:"status"`
	Reason  string       `json:"reason,omitempty"`
	Changes []pullChange `json:"changes,omitempty"`
}

// pullChange is one value that a pull writes. From is the value that the file
// gives now, and a change that adds a section or an override has none.
type pullChange struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	From   string `json:"from,omitempty"`
	To     any    `json:"to"`
}

// planPull compares the Relay policy of each named environment with the
// file. A value that the shared policy already gives stays shared, and a
// different value becomes an override in the environment section. The shared
// policy never changes. An inactive environment keeps its section.
func planPull(value config.Config, project control.Project, names []string) (pullPlan, error) {
	plan := pullPlan{names: names, environments: map[string]*pulledEnvironment{}}
	for _, name := range names {
		remote := environmentOf(project, name)
		if remote == nil || remote.RelayURL == "" {
			plan.environments[name] = &pulledEnvironment{Status: "skipped", Reason: "inactive"}
			continue
		}
		delivery, err := config.Retention(remote.DeliveryTtlSeconds)
		if err != nil {
			return pullPlan{}, fmt.Errorf("the %s delivery retention: %w", name, err)
		}
		attachment, err := config.Retention(remote.AttachmentRetentionSeconds)
		if err != nil {
			return pullPlan{}, fmt.Errorf("the %s attachment retention: %w", name, err)
		}
		server := policyFields(config.RelayPolicy{DeliveryRetention: delivery, AttachmentRetention: attachment})
		entry := &pulledEnvironment{Status: "unchanged"}
		plan.environments[name] = entry
		section := "environments." + name
		if name == "production" && value.Environments.Production == nil {
			// The file has no section for the environment, so each value that
			// the shared policy does not give is added as an override.
			shared := policyFields(value.Relay)
			for _, field := range relayFields {
				if server[field] == shared[field] {
					continue
				}
				entry.Changes = append(entry.Changes, pullChange{Path: section + ".relay." + field, Action: "add", To: server[field]})
				plan.changes = append(plan.changes, config.Change{Path: []string{"environments", name, "relay", field}, Value: server[field]})
			}
			if len(entry.Changes) == 0 {
				entry.Changes = []pullChange{{Path: section, Action: "add", To: map[string]any{}}}
				plan.changes = append(plan.changes, config.Change{Path: []string{"environments", name}, Value: map[string]any{}})
			}
			continue
		}
		current, err := value.RelayPolicyFor(name)
		if err != nil {
			return pullPlan{}, err
		}
		// A new override replaces the shared value that the environment
		// takes now, so it is a replacement too.
		effective := policyFields(current)
		for _, field := range relayFields {
			if server[field] == effective[field] {
				continue
			}
			entry.Changes = append(entry.Changes, pullChange{Path: section + ".relay." + field, Action: "replace", From: effective[field], To: server[field]})
			plan.changes = append(plan.changes, config.Change{Path: []string{"environments", name, "relay", field}, Value: server[field]})
			plan.replaced++
		}
	}
	return plan, nil
}

func policyFields(policy config.RelayPolicy) map[string]string {
	return map[string]string{"deliveryRetention": policy.DeliveryRetention, "attachmentRetention": policy.AttachmentRetention}
}

// data gives each environment with changes the status, and keeps the status
// of the others.
func (p pullPlan) data(status string, changed bool) map[string]any {
	environments := map[string]any{}
	for name, entry := range p.environments {
		result := *entry
		if len(result.Changes) > 0 {
			result.Status = status
		}
		environments[name] = result
	}
	return map[string]any{"changed": changed, "environments": environments}
}

// text lists the changes under heading, one per line.
func (p pullPlan) text(heading string) string {
	var text strings.Builder
	text.WriteString(heading)
	for _, name := range p.names {
		for _, change := range p.environments[name].Changes {
			to, _ := json.Marshal(change.To)
			if change.Action == "add" {
				fmt.Fprintf(&text, "\n  add %s: %s", change.Path, to)
				continue
			}
			fmt.Fprintf(&text, "\n  replace %s: %q -> %s", change.Path, change.From, to)
		}
	}
	return text.String()
}
