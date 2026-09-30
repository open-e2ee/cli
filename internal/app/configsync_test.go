package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/open-e2ee/oe/internal/config"
	"github.com/open-e2ee/oe/internal/control"
	"github.com/open-e2ee/oe/internal/credential"
)

// sandboxOnly is a config with no Production section. The shared policy is
// 30d, and Sandbox overrides both fields with 1d.
const sandboxOnly = `// Public service policy. Do not put secrets in this file.
import { defineConfig } from "@open-e2ee/oe/config";

export default defineConfig({
  product: "signal-relay",
  project: "pull-chat",
  relay: {
    deliveryRetention: "30d",
    attachmentRetention: "30d",
  },
  environments: {
    sandbox: {
      relay: {
        deliveryRetention: "1d",
        attachmentRetention: "1d",
      },
    },
  },
});
`

// retention gives the policy of an active environment in seconds.
func retention(delivery, attachment int) *control.ProjectEnvironment {
	return &control.ProjectEnvironment{
		DeliveryTtlSeconds: delivery, AttachmentRetentionSeconds: attachment,
		RelayURL: "https://relay.example/signal/v1/connection/pk_public", Revision: "4",
	}
}

const (
	hour = 3_600
	day  = 86_400
)

// pullServer is a control API whose project read answers the policy of each
// active environment. A nil environment is not active. It counts the reads.
func pullServer(t *testing.T, slug string, sandbox, production *control.ProjectEnvironment) (Dependencies, *int) {
	t.Helper()
	reads := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/projects/"+slug, func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(response, `{"code":"AUTHENTICATION_REQUIRED"}`, http.StatusUnauthorized)
			return
		}
		reads++
		_ = json.NewEncoder(response).Encode(control.Project{Slug: slug, Writer: "config", Sandbox: sandbox, Production: production})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	api, err := control.New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	store := credential.NewMemory()
	storeCredential(t, store, "project:read")
	return Dependencies{API: api, HTTP: server.Client(), Store: store}, &reads
}

// projectWith writes source as the config file of a new directory.
func projectWith(t *testing.T, source string) (string, string) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, config.Filename)
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return directory, path
}

func mustLoad(t *testing.T, path string) config.Config {
	t.Helper()
	value, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// entry returns data.environments.<name> of a pull result.
func entry(t *testing.T, result event, name string) map[string]any {
	t.Helper()
	environments, _ := result.Data["environments"].(map[string]any)
	value, ok := environments[name].(map[string]any)
	if !ok {
		t.Fatalf("the result has no %s entry: %#v", name, result.Data)
	}
	return value
}

func TestPullAddsWithoutConsent(t *testing.T) {
	directory, path := projectWith(t, sandboxOnly)
	dependencies, _ := pullServer(t, "pull-chat", retention(day, day), retention(7*day, 30*day))
	dependencies.WorkingDir = directory

	exit, stdout, _ := run(t, dependencies, "--json", "config", "pull")
	result := decodeEvent(t, []byte(stdout))
	if exit != 0 || result.Status != "ok" || result.Command != "config pull" || result.Data["changed"] != true {
		t.Fatalf("a pull that only adds did not apply without --yes: exit=%d %s", exit, stdout)
	}
	if status := entry(t, result, "sandbox")["status"]; status != "unchanged" {
		t.Fatalf("sandbox status = %v, want unchanged: %s", status, stdout)
	}
	production := entry(t, result, "production")
	changes, _ := production["changes"].([]any)
	if production["status"] != "applied" || len(changes) != 1 {
		t.Fatalf("production was not applied with one change: %s", stdout)
	}
	if change := changes[0].(map[string]any); change["action"] != "add" || change["path"] != "environments.production.relay.deliveryRetention" || change["to"] != "7d" {
		t.Fatalf("the production change is not the delivery override: %#v", change)
	}
	// The shared policy already gives the attachment retention, so only the
	// delivery retention becomes an override.
	value := mustLoad(t, path)
	if value.Environments.Production == nil || value.Environments.Production.Relay == nil ||
		*value.Environments.Production.Relay != (config.RelayOverride{DeliveryRetention: "7d"}) || value.Relay.DeliveryRetention != "30d" {
		t.Fatalf("the pull did not add the fewest values: %#v", value)
	}

	exit, stdout, _ = run(t, dependencies, "--json", "config", "pull")
	result = decodeEvent(t, []byte(stdout))
	if exit != 0 || result.Data["changed"] != false || entry(t, result, "production")["status"] != "unchanged" {
		t.Fatalf("a second pull changed the file: exit=%d %s", exit, stdout)
	}
}

func TestPullReplacingAValueNeedsYes(t *testing.T) {
	for _, test := range []struct {
		name       string
		production *control.ProjectEnvironment
		path       string
		from       string
	}{
		// Sandbox overrides 1d in the file.
		{"an override", nil, "environments.sandbox.relay.deliveryRetention", "1d"},
		// Production takes the shared 30d; a new override replaces its value.
		{"a shared value", retention(7*day, 30*day), "environments.production.relay.deliveryRetention", "30d"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := initializedProject(t, "pull-chat")
			path := filepath.Join(directory, config.Filename)
			before := mustRead(t, path)
			sandbox := retention(day, day)
			if test.production == nil {
				sandbox = retention(3*day, day)
			}
			dependencies, _ := pullServer(t, "pull-chat", sandbox, test.production)
			dependencies.WorkingDir = directory

			exit, stdout, _ := run(t, dependencies, "--json", "config", "pull")
			refusal := decodeEvent(t, []byte(stdout))
			if exit != exitUsage || refusal.Code != "CONFIRMATION_REQUIRED" || refusal.Next != "oe config pull --yes" {
				t.Fatalf("a replaced value did not need --yes: exit=%d %s", exit, stdout)
			}
			if !strings.Contains(stdout, `"path":"`+test.path+`"`) || !strings.Contains(stdout, `"from":"`+test.from+`"`) {
				t.Fatalf("the refusal does not list the replaced value: %s", stdout)
			}
			if after := mustRead(t, path); string(after) != string(before) {
				t.Fatalf("CONFIRMATION_REQUIRED changed the file:\n%s", after)
			}

			// A person at a terminal answers the prompt instead.
			person := dependencies
			person.In, person.Interactive = strings.NewReader("n\n"), terminal
			exit, _, stderr := run(t, person, "config", "pull")
			if exit != exitFailure || !strings.Contains(stderr, "Replace 1 value(s)") || !strings.Contains(stderr, test.path) {
				t.Fatalf("a person who declined was not asked or did not stop the pull: exit=%d %s", exit, stderr)
			}
			if after := mustRead(t, path); string(after) != string(before) {
				t.Fatalf("a declined pull changed the file:\n%s", after)
			}

			exit, stdout, _ = run(t, dependencies, "--json", "config", "pull", "--yes")
			if result := decodeEvent(t, []byte(stdout)); exit != 0 || result.Data["changed"] != true {
				t.Fatalf("pull --yes did not apply: exit=%d %s", exit, stdout)
			}
			if !strings.Contains(string(mustRead(t, path)), `deliveryRetention: "`) {
				t.Fatal("the pull removed the delivery retention")
			}
			policy, err := mustLoad(t, path).RelayPolicyFor(strings.Split(test.path, ".")[1])
			if err != nil || (policy.DeliveryRetention != "3d" && policy.DeliveryRetention != "7d") {
				t.Fatalf("pull --yes did not write the server value: %#v %v", policy, err)
			}
		})
	}

	t.Run("with --env", func(t *testing.T) {
		directory := initializedProject(t, "pull-chat")
		dependencies, _ := pullServer(t, "pull-chat", retention(3*day, day), nil)
		dependencies.WorkingDir = directory
		exit, stdout, _ := run(t, dependencies, "--json", "config", "pull", "-e", "sandbox")
		if refusal := decodeEvent(t, []byte(stdout)); exit != exitUsage || refusal.Next != "oe config pull --yes --env sandbox" {
			t.Fatalf("the next command dropped --env: exit=%d %s", exit, stdout)
		}
	})
}

func TestPullKeepsComments(t *testing.T) {
	source := `// Public service policy. Do not put secrets in this file.
import { defineConfig } from "@open-e2ee/oe/config";

/* The Relay policy of every environment. */
export default defineConfig({
  product: "signal-relay",
  project: "pull-chat", // the console slug
  relay: {
    // Keep a month of history.
    deliveryRetention: "30d",
    attachmentRetention: "30d", /* attachments too */
  },
  environments: {
    sandbox: {
      relay: {
        deliveryRetention: "1d", // short for tests
        attachmentRetention: "1d",
      },
    },
    // Production takes the shared policy.
    production: {},
  },
});
`
	directory, path := projectWith(t, source)
	dependencies, _ := pullServer(t, "pull-chat", retention(3*day, day), retention(30*day, 14*day))
	dependencies.WorkingDir = directory

	exit, stdout, _ := run(t, dependencies, "--json", "config", "pull", "--yes")
	if exit != 0 {
		t.Fatalf("pull --yes failed: exit=%d %s", exit, stdout)
	}
	want := strings.Replace(source, `deliveryRetention: "1d", // short for tests`, `deliveryRetention: "3d", // short for tests`, 1)
	want = strings.Replace(want, `production: {},`, `production: {
      relay: {
        attachmentRetention: "14d",
      },
    },`, 1)
	if got := string(mustRead(t, path)); got != want {
		t.Fatalf("the pull changed more than the values\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestPullOfAComputedValueWritesNothingAndReturnsTheEdits(t *testing.T) {
	source := strings.Replace(sandboxOnly, `import { defineConfig } from "@open-e2ee/oe/config";
`, `import { defineConfig } from "@open-e2ee/oe/config";

const short = ["1", "d"].join("");
`, 1)
	source = strings.Replace(source, `        deliveryRetention: "1d",
        attachmentRetention: "1d",`, `        deliveryRetention: short,
        attachmentRetention: short,`, 1)
	directory, path := projectWith(t, source)
	// Both computed Sandbox values change, and Production adds a section
	// that the splicer could write.
	dependencies, _ := pullServer(t, "pull-chat", retention(3*day, 12*hour), retention(7*day, 30*day))
	dependencies.WorkingDir = directory

	for _, args := range [][]string{{"--json", "config", "pull"}, {"--json", "config", "pull", "--yes"}} {
		exit, stdout, _ := run(t, dependencies, args...)
		refusal := decodeEvent(t, []byte(stdout))
		if exit != exitPersonAction || refusal.Code != "CONFIG_EDIT_REQUIRED" {
			t.Fatalf("%v: a computed value was not CONFIG_EDIT_REQUIRED: exit=%d %s", args, exit, stdout)
		}
		edits, _ := refusal.Data["edits"].([]any)
		var got []string
		for _, edit := range edits {
			edit := edit.(map[string]any)
			if edit["currentExpression"] != "short" || edit["file"] != path {
				t.Fatalf("%v: the edit does not name the expression and the file: %#v", args, edit)
			}
			got = append(got, edit["path"].(string)+"="+edit["newValue"].(string))
		}
		slices.Sort(got)
		if want := []string{"environments.sandbox.relay.attachmentRetention=12h", "environments.sandbox.relay.deliveryRetention=3d"}; !slices.Equal(got, want) {
			t.Fatalf("%v: edits = %v, want %v: %s", args, got, want, stdout)
		}
		if after := mustRead(t, path); string(after) != source {
			t.Fatalf("%v: CONFIG_EDIT_REQUIRED changed the file:\n%s", args, after)
		}
	}
}

func TestPullIgnoresOEEnv(t *testing.T) {
	for _, selected := range []string{"sandbox", "production", "stage"} {
		t.Run(selected, func(t *testing.T) {
			directory, path := projectWith(t, sandboxOnly)
			dependencies, _ := pullServer(t, "pull-chat", retention(day, day), retention(7*day, 30*day))
			dependencies.WorkingDir = directory
			dependencies.Getenv = environment(map[string]string{"OE_ENV": selected})

			exit, stdout, _ := run(t, dependencies, "--json", "config", "pull")
			result := decodeEvent(t, []byte(stdout))
			if exit != 0 || entry(t, result, "sandbox")["status"] != "unchanged" || entry(t, result, "production")["status"] != "applied" {
				t.Fatalf("OE_ENV=%s narrowed the pull: exit=%d %s", selected, exit, stdout)
			}
			if mustLoad(t, path).Environments.Production == nil {
				t.Fatalf("OE_ENV=%s kept the Production section out of the file", selected)
			}
		})
	}

	t.Run("--env narrows", func(t *testing.T) {
		directory, path := projectWith(t, sandboxOnly)
		dependencies, _ := pullServer(t, "pull-chat", retention(day, day), retention(7*day, 30*day))
		dependencies.WorkingDir = directory
		exit, stdout, _ := run(t, dependencies, "--json", "config", "pull", "--env", "sandbox")
		result := decodeEvent(t, []byte(stdout))
		environments, _ := result.Data["environments"].(map[string]any)
		if _, ok := environments["production"]; exit != 0 || ok || result.Data["changed"] != false {
			t.Fatalf("--env sandbox did not narrow the pull: exit=%d %s", exit, stdout)
		}
		if after := mustRead(t, path); string(after) != sandboxOnly {
			t.Fatalf("--env sandbox changed the file:\n%s", after)
		}
	})
}

func TestPullDryRunWritesNothing(t *testing.T) {
	directory := initializedProject(t, "pull-chat")
	path := filepath.Join(directory, config.Filename)
	before := mustRead(t, path)
	dependencies, _ := pullServer(t, "pull-chat", retention(3*day, day), retention(7*day, 30*day))
	dependencies.WorkingDir = directory

	// A replacement needs no --yes in a dry run, because nothing is written.
	exit, stdout, _ := run(t, dependencies, "--json", "config", "pull", "--dry-run")
	result := decodeEvent(t, []byte(stdout))
	if exit != 0 || result.Data["changed"] != false || entry(t, result, "sandbox")["status"] != "planned" || entry(t, result, "production")["status"] != "planned" {
		t.Fatalf("the dry run did not report the plan: exit=%d %s", exit, stdout)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if after := mustRead(t, path); string(after) != string(before) || len(entries) != 1 {
		t.Fatalf("the dry run wrote to the directory: %v\n%s", entries, after)
	}

	// A dry run reports the edits that a person must make, and exits 0.
	computed, computedPath := projectWith(t, strings.Replace(sandboxOnly, `deliveryRetention: "1d"`, `deliveryRetention: ["1", "d"].join("")`, 1))
	dependencies.WorkingDir = computed
	exit, stdout, _ = run(t, dependencies, "--json", "config", "pull", "--dry-run")
	result = decodeEvent(t, []byte(stdout))
	if edits, _ := result.Data["edits"].([]any); exit != 0 || len(edits) != 1 {
		t.Fatalf("the dry run did not report the computed value: exit=%d %s", exit, stdout)
	}
	if after := string(mustRead(t, computedPath)); !strings.Contains(after, `["1", "d"].join("")`) || strings.Contains(after, "production") {
		t.Fatalf("the dry run changed the file:\n%s", after)
	}
}

func TestPullKeepsTheSectionOfAnInactiveEnvironment(t *testing.T) {
	directory := initializedProject(t, "pull-chat")
	path := filepath.Join(directory, config.Filename)
	before := mustRead(t, path)
	dependencies, reads := pullServer(t, "pull-chat", retention(day, day), nil)
	dependencies.WorkingDir = directory

	exit, stdout, _ := run(t, dependencies, "--json", "config", "pull")
	result := decodeEvent(t, []byte(stdout))
	production := entry(t, result, "production")
	if exit != 0 || result.Data["changed"] != false || production["status"] != "skipped" || production["reason"] != "inactive" || *reads != 1 {
		t.Fatalf("an inactive Production was not skipped: exit=%d reads=%d %s", exit, *reads, stdout)
	}
	if after := mustRead(t, path); string(after) != string(before) {
		t.Fatalf("the pull removed or changed the Production section:\n%s", after)
	}
}
