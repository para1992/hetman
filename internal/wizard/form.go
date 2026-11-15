package wizard

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"hetman/internal/i18n"
	"hetman/internal/runinput"
)

// ErrCancelled is returned by Form when the person cancels it or the input
// ends before they confirm.
var ErrCancelled = errors.New("the form was cancelled") // i18n: keep (the commands print their own message)

// form asks for a run input with the plumbing of the wizard.
type form struct {
	w    *wizard
	spec *runinput.Spec
	code string // the language labels are shown in
}

// Form asks a person for the run input the orders describe, one field at a
// time, shows what was given, and returns it once confirmed, checked by
// runinput.Check. Fields with ask: false are not asked; they get their
// default. echo writes every answer read, as the wizard does.
//
// Nothing here calls a model; a later step that turns prose into fields
// would fill the same values before they are shown for confirmation.
func Form(p *i18n.Printer, spec *runinput.Spec, in io.Reader, out io.Writer, echo bool) (json.RawMessage, error) {
	f := &form{w: &wizard{in: bufio.NewReader(in), out: out, echo: echo, p: p}, spec: spec, code: p.Lang()}
	fmt.Fprintln(out, p.T("form.title"))
	values := map[string]any{}
	for _, fd := range spec.Fields {
		if !fd.Ask {
			continue
		}
		v, ok, err := f.field(fd, nil, "")
		if err != nil {
			return nil, ErrCancelled
		}
		if ok {
			values[fd.Name] = v
		}
	}
	for {
		data, _ := json.Marshal(values)
		checked, err := runinput.Check(spec, data)
		fmt.Fprintf(out, "\n%s\n", p.T("form.summary"))
		if err != nil {
			fmt.Fprint(out, runinput.Write(p, spec, data))
			var inv *runinput.Invalid
			if errors.As(err, &inv) {
				for _, line := range inv.Lines(p) {
					fmt.Fprintln(out, "  ! "+line)
				}
			}
		} else {
			fmt.Fprint(out, runinput.Write(p, spec, checked))
		}
		use, change, cancel := p.T("form.action.use"), p.T("form.action.change"), p.T("form.action.cancel")
		options := []string{use, change, cancel}
		def := use
		if err != nil {
			options, def = []string{change, cancel}, change
		}
		pick, perr := f.w.choose(p.T("form.what_now"), options, def)
		switch {
		case perr != nil || pick == cancel:
			return nil, ErrCancelled
		case pick == use:
			return checked, nil
		}
		if err := f.change(values); err != nil {
			return nil, ErrCancelled
		}
	}
}

// change lets the person change one top-level field.
func (f *form) change(values map[string]any) error {
	var asked []*runinput.Field
	var names []string
	for _, fd := range f.spec.Fields {
		if fd.Ask {
			asked = append(asked, fd)
			names = append(names, fd.LabelIn(f.code))
		}
	}
	pick, err := f.w.choose(f.w.p.T("form.which_field"), names, "")
	if err != nil {
		return err
	}
	for i, n := range names {
		if n != pick {
			continue
		}
		fd := asked[i]
		if fd.Kind == runinput.Objects {
			return f.changeObjects(fd, values)
		}
		v, ok, err := f.field(fd, values[fd.Name], "")
		if err != nil {
			return err
		}
		if ok {
			values[fd.Name] = v
		} else {
			delete(values, fd.Name)
		}
	}
	return nil
}

// changeObjects changes, adds or removes one item of a list of objects.
func (f *form) changeObjects(fd *runinput.Field, values map[string]any) error {
	p := f.w.p
	list, _ := values[fd.Name].([]any)
	label := fd.LabelIn(f.code)
	var options []string
	for i, item := range list {
		options = append(options, p.T("form.change_item", "item", f.itemName(fd, i, item)))
	}
	add, remove, back := p.T("form.add_item"), p.T("form.remove_item"), p.T("form.back")
	options = append(options, add)
	if len(list) > 0 {
		options = append(options, remove)
	}
	options = append(options, back)
	pick, err := f.w.choose(p.T("form.which_item", "label", label), options, back)
	if err != nil {
		return err
	}
	switch pick {
	case back:
		return nil
	case add:
		item, err := f.object(fd, len(list)+1, nil)
		if err != nil {
			return err
		}
		values[fd.Name] = append(list, item)
		return nil
	case remove:
		var names []string
		for i, item := range list {
			names = append(names, f.itemName(fd, i, item))
		}
		gone, err := f.w.choose(p.T("form.which_remove"), names, "")
		if err != nil {
			return err
		}
		for i, n := range names {
			if n == gone {
				values[fd.Name] = append(list[:i:i], list[i+1:]...)
				break
			}
		}
		return nil
	}
	for i, item := range list {
		if options[i] != pick {
			continue
		}
		cur, _ := item.(map[string]any)
		changed, err := f.object(fd, i+1, cur)
		if err != nil {
			return err
		}
		list[i] = changed
	}
	return nil
}

// itemName names an item of a list of objects by its number and its first
// text.
func (f *form) itemName(fd *runinput.Field, i int, item any) string {
	name := f.w.p.T("form.item", "label", fd.LabelIn(f.code), "n", i+1)
	obj, _ := item.(map[string]any)
	for _, sub := range fd.Fields {
		if s, ok := obj[sub.Name].(string); ok && s != "" && sub.Kind == runinput.Text {
			return name + " (" + s + ")"
		}
	}
	return name
}

// prompt is how a field is asked: its label, and whether it may be left
// empty.
func (f *form) prompt(fd *runinput.Field, prefix string) string {
	s := prefix + fd.LabelIn(f.code)
	if !fd.Required {
		s += " " + f.w.p.T("form.optional")
	}
	return s
}

// hint writes the hint of a field before it is asked.
func (f *form) hint(fd *runinput.Field, prefix string) {
	if h := fd.Hint.In(f.code); h != "" {
		fmt.Fprintf(f.w.out, "%s  (%s)\n", prefix, h)
	}
}

// field asks one field. cur is its value so far, offered again; without
// one the default is offered. It reports false when the field is left
// empty.
func (f *form) field(fd *runinput.Field, cur any, prefix string) (any, bool, error) {
	w, p := f.w, f.w.p
	if cur == nil {
		cur = fd.Default
	}
	f.hint(fd, prefix)
	prompt := f.prompt(fd, prefix)
	switch fd.Kind {
	case runinput.Text:
		def, _ := cur.(string)
		for {
			s, err := w.ask(prompt, def)
			if err != nil {
				return nil, false, err
			}
			if s != "" {
				return s, true, nil
			}
			if !fd.Required {
				return nil, false, nil
			}
			fmt.Fprintln(w.out, p.T("wizard.need_answer"))
		}
	case runinput.LongText:
		def, _ := cur.(string)
		return f.longText(fd, prompt, def, prefix)
	case runinput.Number:
		def := ""
		if n, ok := cur.(json.Number); ok {
			def = string(n)
		}
		for {
			s, err := w.ask(prompt, def)
			if err != nil {
				return nil, false, err
			}
			if s == "" {
				if !fd.Required {
					return nil, false, nil
				}
				fmt.Fprintln(w.out, p.T("wizard.need_answer"))
				continue
			}
			var n json.Number
			if _, perr := strconv.ParseFloat(s, 64); perr == nil && json.Unmarshal([]byte(s), &n) == nil {
				return n, true, nil
			}
			fmt.Fprintln(w.out, p.T("form.not_a_number"))
		}
	case runinput.YesNo:
		def := ""
		if b, ok := cur.(bool); ok {
			def = p.T("wizard.no_short")
			if b {
				def = p.T("wizard.yes_short")
			}
		}
		for {
			s, err := w.ask(prompt+" "+p.T("wizard.yes_no"), def)
			if err != nil {
				return nil, false, err
			}
			switch {
			case s == "" && !fd.Required:
				return nil, false, nil
			case i18n.IsYes(p, s):
				return true, true, nil
			case i18n.IsNo(p, s):
				return false, true, nil
			}
			fmt.Fprintln(w.out, p.T("wizard.answer_yes_no"))
		}
	case runinput.Choice:
		def, _ := cur.(string)
		fmt.Fprintln(w.out, prompt)
		for i, o := range fd.Options {
			fmt.Fprintf(w.out, "%s  %d) %s\n", prefix, i+1, o)
		}
		for {
			s, err := w.ask(prefix+p.T("wizard.choice"), def)
			if err != nil {
				return nil, false, err
			}
			if s == "" && !fd.Required {
				return nil, false, nil
			}
			if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(fd.Options) {
				return fd.Options[n-1], true, nil
			}
			for _, o := range fd.Options {
				if o == s {
					return o, true, nil
				}
			}
			fmt.Fprintln(w.out, p.T("wizard.pick_number", "from", 1, "to", len(fd.Options)))
		}
	case runinput.List:
		return f.list(fd, prompt, cur, prefix)
	case runinput.Objects:
		return f.objects(fd, prefix)
	}
	return nil, false, nil
}

// longText reads lines until an empty one.
func (f *form) longText(fd *runinput.Field, prompt, def, prefix string) (any, bool, error) {
	w, p := f.w, f.w.p
	if def != "" {
		fmt.Fprintln(w.out, prefix+p.T("form.long_keep"))
		for _, line := range strings.Split(def, "\n") {
			fmt.Fprintf(w.out, "%s    %s\n", prefix, line)
		}
	}
	fmt.Fprintln(w.out, prefix+p.T("form.long_hint"))
	for {
		var lines []string
		for {
			label := prompt
			if len(lines) > 0 {
				label = prefix + "  …"
			}
			s, err := w.ask(label, "")
			if err != nil {
				return nil, false, err
			}
			if s == "" {
				break
			}
			lines = append(lines, s)
		}
		switch {
		case len(lines) > 0:
			return strings.Join(lines, "\n"), true, nil
		case def != "":
			return def, true, nil
		case !fd.Required:
			return nil, false, nil
		}
		fmt.Fprintln(w.out, p.T("wizard.need_answer"))
	}
}

// list reads the items of a list of texts until an empty answer.
func (f *form) list(fd *runinput.Field, prompt string, cur any, prefix string) (any, bool, error) {
	w, p := f.w, f.w.p
	old, _ := cur.([]any)
	fmt.Fprintln(w.out, prompt)
	if len(old) > 0 {
		var items []string
		for _, it := range old {
			items = append(items, fmt.Sprint(it))
		}
		fmt.Fprintln(w.out, prefix+"  "+p.T("form.list_keep", "items", strings.Join(items, "; ")))
	}
	fmt.Fprintln(w.out, prefix+"  "+p.T("form.list_hint"))
	for {
		items := []any{}
		for {
			s, err := w.ask(fmt.Sprintf("%s  %d", prefix, len(items)+1), "")
			if err != nil {
				return nil, false, err
			}
			if s == "" {
				break
			}
			items = append(items, s)
		}
		switch {
		case len(items) > 0:
			return items, true, nil
		case len(old) > 0:
			return old, true, nil
		case !fd.Required:
			return nil, false, nil
		}
		fmt.Fprintln(w.out, p.T("wizard.need_answer"))
	}
}

// objects asks item after item of a list of objects.
func (f *form) objects(fd *runinput.Field, prefix string) (any, bool, error) {
	p := f.w.p
	label := fd.LabelIn(f.code)
	items := []any{}
	for {
		if len(items) > 0 || !fd.Required {
			key := "form.another"
			if len(items) == 0 {
				key = "form.add_first"
			}
			more, err := f.w.yesno(prefix+p.T(key, "label", label), false)
			if err != nil {
				return nil, false, err
			}
			if !more {
				break
			}
		}
		item, err := f.object(fd, len(items)+1, nil)
		if err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, false, nil
	}
	return items, true, nil
}

// object asks the fields of item n of a list of objects; cur holds its
// values so far when it is changed.
func (f *form) object(fd *runinput.Field, n int, cur map[string]any) (map[string]any, error) {
	fmt.Fprintf(f.w.out, "  %s\n", f.w.p.T("form.item", "label", fd.LabelIn(f.code), "n", n))
	obj := map[string]any{}
	for _, sub := range fd.Fields {
		if !sub.Ask {
			continue
		}
		v, ok, err := f.field(sub, cur[sub.Name], "    ")
		if err != nil {
			return nil, err
		}
		if ok {
			obj[sub.Name] = v
		}
	}
	return obj, nil
}
