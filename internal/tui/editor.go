package tui

import "strings"

// editor is a single-line text field.
//
// Hand-written rather than taken from a widget library, and the reason is one
// line long: a text input is thirty lines of rune arithmetic, and the dependency
// that would save them brings a component model, a focus model and a styling
// model that this package already has its own answers to.
//
// It is a value, and every operation returns a new one. bubbletea hands Update a
// copy of the model and keeps what it returns, so a field that mutated in place
// would let an abandoned form's edits survive being abandoned.
type editor struct {
	value string

	// cursor is a rune index, never a byte one. The values here are working
	// directories and branch names, which are the two places a user's own
	// language shows up.
	cursor int
}

func editorOf(s string) editor {
	return editor{value: s, cursor: len([]rune(s))}
}

func (e editor) runes() []rune { return []rune(e.value) }

// clamp keeps the cursor inside the value, which insertion and deletion can
// both push it out of.
func (e editor) clamp() editor {
	n := len(e.runes())
	if e.cursor < 0 {
		e.cursor = 0
	}
	if e.cursor > n {
		e.cursor = n
	}
	return e
}

func (e editor) insert(s string) editor {
	e = e.clamp()
	r := e.runes()
	var b strings.Builder
	b.WriteString(string(r[:e.cursor]))
	b.WriteString(s)
	b.WriteString(string(r[e.cursor:]))
	return editor{value: b.String(), cursor: e.cursor + len([]rune(s))}
}

func (e editor) backspace() editor {
	e = e.clamp()
	if e.cursor == 0 {
		return e
	}
	r := e.runes()
	return editor{value: string(r[:e.cursor-1]) + string(r[e.cursor:]), cursor: e.cursor - 1}
}

// remove deletes the rune under the cursor. Named for what it does to text and
// not for what it does to anything on disk — this package touches none.
func (e editor) remove() editor {
	e = e.clamp()
	r := e.runes()
	if e.cursor >= len(r) {
		return e
	}
	return editor{value: string(r[:e.cursor]) + string(r[e.cursor+1:]), cursor: e.cursor}
}

func (e editor) left() editor {
	e = e.clamp()
	if e.cursor > 0 {
		e.cursor--
	}
	return e
}

func (e editor) right() editor {
	e = e.clamp()
	if e.cursor < len(e.runes()) {
		e.cursor++
	}
	return e
}

func (e editor) home() editor { return editor{value: e.value} }

func (e editor) end() editor { return editorOf(e.value) }

// display renders the value with a caret at the cursor, for a focused field.
//
// The caret is a character inserted into the text rather than a styled cell,
// because everything that measures a line in this package measures the string it
// is handed — and a cursor drawn by inverting a cell would be a cell the width
// arithmetic never saw.
func (e editor) display(focused bool) string {
	if !focused {
		return e.value
	}
	c := e.clamp()
	r := c.runes()
	return string(r[:c.cursor]) + "▏" + string(r[c.cursor:])
}
