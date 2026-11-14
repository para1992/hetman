package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/chronicle"
	"hetman/internal/language"
	"hetman/internal/orders"
)

// The opening words of the language instruction.
const instructionStart = "Write all text meant for a person in "

// withLanguage adds a language key after the start of a config.
func withLanguage(config, code string) string {
	return strings.Replace(config, "start: a\n", "start: a\nlanguage: "+code+"\n", 1)
}

// languagePlan asks one round of questions, then gives an output the schema
// rejects, then a valid one.
const languagePlan = `{"trace": "trace.jsonl", "nodes": {"a": [
	[{"session": "s-1"}, {"result": ` + q2 + `}],
	[{"session": "s-1"}, {"result": {"outcome": "maybe"}}],
	[{"session": "s-1"}, {"result": {"outcome": "ok"}}]]}}`

// runLanguageDialogue drives a dialogue step through a round of questions
// and a retry, and returns the prompt of every attempt as the adapter got
// it, after checking that the stored prompt is the same.
func runLanguageDialogue(t *testing.T, config string) []string {
	f := setupAgent(t, config, agentFiles(languagePlan), "")
	done := f.driveAsync(context.Background())
	f.waitAsked(0)
	f.answer(map[string]string{"sure": "yes"}, nil, "")
	f.wait(done, Done)
	want(t, f.finished(), []string{"a#1 /", "a#2 /invalid_output", "a#3 ok/"})
	var prompts []string
	for i, s := range f.steps() {
		if stored := f.file(PromptFile("a", i+1)); stored != s.Prompt {
			t.Fatalf("attempt %d: stored prompt %q, sent %q", i+1, stored, s.Prompt)
		}
		prompts = append(prompts, s.Prompt)
	}
	if len(prompts) != 3 {
		t.Fatalf("%d attempts", len(prompts))
	}
	return prompts
}

func TestLanguageInstructionOncePerAttempt(t *testing.T) {
	prompts := runLanguageDialogue(t, withLanguage(dialogue("1", "3"), "hi"))
	instruction := languageInstruction(mustLanguage(t, "hi"))
	if !strings.Contains(instruction, instructionStart+"Hindi (हिन्दी): questions for the person") {
		t.Fatalf("instruction %q", instruction)
	}
	for i, p := range prompts {
		if n := strings.Count(p, instructionStart); n != 1 || !strings.Contains(p, instruction) {
			t.Errorf("attempt %d holds the instruction %d times:\n%s", i+1, n, p)
		}
	}
	if !strings.HasPrefix(prompts[0], "Do it."+instruction+"\n\n---\nIf you cannot finish this step well") {
		t.Errorf("first attempt:\n%s", prompts[0])
	}
	if !strings.HasPrefix(prompts[1], "The person answered your questions (round 1)") {
		t.Errorf("second attempt:\n%s", prompts[1])
	}
	if !strings.Contains(prompts[2], "(attempt 2) was rejected") {
		t.Errorf("third attempt:\n%s", prompts[2])
	}
}

// TestNoLanguageChangesNothing: without the key the prompts are exactly
// the template and the engine's own additions, as before languages existed.
func TestNoLanguageChangesNothing(t *testing.T) {
	prompts := runLanguageDialogue(t, dialogue("1", "3"))
	if prompts[0] != "Do it."+askInstructions(1) {
		t.Errorf("first attempt %q", prompts[0])
	}
	wantSecond := "The person answered your questions (round 1):\n- Sure? (sure)\n  Answer: yes\n\n" +
		"You may not ask any more questions. Otherwise continue the step and reply with a single JSON object that matches the required output schema.\n"
	if prompts[1] != wantSecond {
		t.Errorf("second attempt %q\nwant %q", prompts[1], wantSecond)
	}
	if !strings.HasPrefix(prompts[2], wantSecond+"\n\n---\nYour previous answer (attempt 2) was rejected") {
		t.Errorf("third attempt %q", prompts[2])
	}
	for i, p := range prompts {
		if strings.Contains(p, instructionStart) {
			t.Errorf("attempt %d has a language instruction:\n%s", i+1, p)
		}
	}
}

func TestLanguagePlaceholders(t *testing.T) {
	files := map[string]string{"p.md": "For a reader of {{language}} / {{ language.native }} / {{language.code}}.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"a": [[{"result": {"outcome": "ok"}}]]}}`}
	f := setupAgent(t, one(""), files, "")
	f.drive(Done)
	if p := f.steps()[0].Prompt; p != "For a reader of English / English / en." {
		t.Fatalf("without a language: %q", p)
	}
	f = setupAgent(t, withLanguage(one(""), "uk"), files, "")
	f.drive(Done)
	if p := f.steps()[0].Prompt; p != "For a reader of Ukrainian / українська / uk."+languageInstruction(mustLanguage(t, "uk")) {
		t.Fatalf("with a language: %q", p)
	}
}

// TestLanguageReachesScriptsAndHook: every command of a campaign with a
// language gets its code in HETMAN_LANGUAGE, whatever the engine's own
// environment says; a campaign without one passes none.
func TestLanguageReachesScriptsAndHook(t *testing.T) {
	t.Setenv("HETMAN_LANGUAGE", "inherited")
	config := `version: 0
start: a
notify: {run: ` + arg("env") + `}
checks:
  c: {run: ` + arg("env") + `}
nodes:
  a: {type: script, run: ` + arg("env") + `, on: {ok: g, fail: failed}}
  g:
    type: gate
    checks: [c]
    ask: [{id: fine, question: "Fine?"}]
    on: {pass: done, blocked: failed}
`
	for _, code := range []string{"es", ""} {
		c := config
		if code != "" {
			c = withLanguage(config, code)
		}
		f := setupAgent(t, c, nil, "")
		done := f.driveAsync(context.Background())
		f.waitAsked(0)
		f.answer(map[string]string{"fine": "yes"}, nil, "")
		f.wait(done, Done)
		f.e.WaitHooks()
		// The check and then the hook of g write env.g.1.json; the hook is
		// the later one, as it starts when the gate asks.
		for _, file := range []string{"env.a.1.json", "env.g.1.json"} {
			vars := readEnv(t, filepath.Join(f.work, file))
			if got, ok := vars["HETMAN_LANGUAGE"]; got != code || ok != (code != "") {
				t.Errorf("language %q: %s has HETMAN_LANGUAGE=%q (set %v)", code, file, got, ok)
			}
			if file == "env.g.1.json" && vars["HETMAN_REASON"] != chronicle.ReasonGate {
				t.Errorf("%s was not written by the hook: %v", file, vars)
			}
		}
	}
}

// TestLanguageRecordedAtStartSurvivesResume: the language a campaign started
// with stays when the orders change before it resumes.
func TestLanguageRecordedAtStartSurvivesResume(t *testing.T) {
	files := map[string]string{"p.md": "Do it in {{language}}.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"a": [[{"result": {"outcome": "ok"}}]]}}`}
	f := setupAgent(t, withLanguage(one(""), "zh"), files, "")
	// The engine died during the first attempt; then the language was
	// changed in the file, and removed from it.
	f.record(chronicle.Event{Kind: chronicle.StepStarted, Node: "a", Attempt: 1})
	for _, config := range []string{withLanguage(one(""), "ar"), one("")} {
		if err := os.WriteFile(f.e.Orders.Path, []byte(strings.Replace(config, "PLAN", `[`+strings.Join(quoted(f.e.Orders.Executors["stub"].Run), ", ")+`]`, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		o, err := orders.Load(f.e.Orders.Path)
		if err != nil {
			t.Fatal(err)
		}
		f.e = &Engine{Orders: o, Chron: f.chron, Campaign: f.e.Campaign}
	}
	if f.e.Orders.Language != "" {
		t.Fatalf("the orders still name %q", f.e.Orders.Language)
	}
	f.drive(Done)
	if p := f.steps()[0].Prompt; p != "Do it in Mandarin Chinese."+languageInstruction(mustLanguage(t, "zh")) {
		t.Fatalf("resumed prompt %q", p)
	}
}

func quoted(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		data, _ := json.Marshal(s)
		out[i] = string(data)
	}
	return out
}

func readEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var vars map[string]string
	if err := json.Unmarshal(data, &vars); err != nil {
		t.Fatal(err)
	}
	return vars
}

func mustLanguage(t *testing.T, code string) language.Language {
	t.Helper()
	l, ok := language.Lookup(code)
	if !ok {
		t.Fatalf("no language %q", code)
	}
	return l
}
