package wizard

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/i18n"
	"hetman/internal/orders"
)

// A wizard session in Ukrainian: adding one step after an existing one.
// The person answers by number, by a Ukrainian word, by a type name that
// stays Latin, and with "т" for yes.
func TestUkrainianSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), orders.DefaultFile)
	if err := os.WriteFile(path, []byte(fourSteps), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	answers := []string{
		"додати крок", "script", "notes", "після кроку", "publish", "sh steps/notes.sh", "", "", "т",
		"вийти",
	}
	if err := RunIn(i18n.For("uk"), path, input(answers...), &out, true); err != nil {
		t.Fatal(err)
	}
	o, err := orders.Load(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if o.Nodes["notes"] == nil || o.Nodes["publish"].On["ok"] != "notes" {
		t.Fatalf("the step was not added:\n%s", out.String())
	}
	for _, s := range []string{
		"Майстер Писаря для " + path + ". Модель не використовується.",
		"Що далі?", "  1) додати крок", "  11) вийти",
		"Який це крок?", "  1) script (запускає команду)",
		"Назва кроку (латинські літери, цифри, - _ .): notes",
		"Куди його поставити?", "Після якого кроку?",
		"Команда для запуску (програма й аргументи) [sh steps/notes.sh]:",
		"Обмеження часу, наприклад 5m (Enter — без обмеження):",
		"Якщо не вдасться, перейти до",
		"Зміна:", "  - додати вузол script notes після publish, запускає sh steps/notes.sh",
		"+  notes:",
		"Записати? (т/н) [т]: т",
		"Записано orders.yaml.",
	} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("transcript lacks %q:\n%s", s, out.String())
		}
	}
}
