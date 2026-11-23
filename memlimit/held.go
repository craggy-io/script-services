package memlimit

import (
	"reflect"
	"unsafe"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

// Approximate sizes, in bytes, of what values take beyond their contents.
const (
	slotBytes   = 16 // an interface: one element of a list, tuple or frame
	headerBytes = 16 // a boxed string, bytes or small object
	entryBytes  = 64 // one dict or set entry with its share of the table
	objectBytes = 48 // a list, dict, set, struct or function itself
	// Strings at least this long are counted once however many places
	// hold them; shorter ones are counted where they are held.
	shareBytes = 64
)

// Held estimates the bytes of Starlark values thread holds: everything
// reachable from its call stack, each value counted once. It must run on
// the thread's own goroutine while the thread is stopped at a checkpoint
// (from OnMaxSteps, or a built-in it called), and it may be slow in
// proportion to what is held.
func Held(thread *starlark.Thread) int64 {
	w := walker{seen: map[unsafe.Pointer]bool{}, modules: map[*starlark.Module]bool{}}
	for i := 0; i < thread.CallStackDepth(); i++ {
		fr := thread.DebugFrame(i)
		switch fn := fr.Callable().(type) {
		case *starlark.Function:
			slots := frameSlots(fr, fn)
			w.n += int64(len(slots)) * slotBytes
			for _, v := range slots {
				w.push(v)
			}
			w.push(fn)
		case *starlark.Builtin:
			w.push(fn)
		}
	}
	w.drain()
	return w.n
}

type walker struct {
	n       int64
	todo    []starlark.Value
	seen    map[unsafe.Pointer]bool
	modules map[*starlark.Module]bool
}

func (w *walker) push(v starlark.Value) {
	if v != nil {
		w.todo = append(w.todo, v)
	}
}

// first reports whether p hasn't been counted yet, and marks it counted.
func (w *walker) first(p unsafe.Pointer) bool {
	if p == nil || w.seen[p] {
		return false
	}
	w.seen[p] = true
	return true
}

func (w *walker) drain() {
	for len(w.todo) > 0 {
		v := w.todo[len(w.todo)-1]
		w.todo = w.todo[:len(w.todo)-1]
		w.value(v)
	}
}

func (w *walker) str(n int, data unsafe.Pointer) {
	w.n += headerBytes
	if n < shareBytes || w.first(data) {
		w.n += int64(n)
	}
}

func (w *walker) value(v starlark.Value) {
	switch v := v.(type) {
	case starlark.String:
		w.str(len(v), unsafe.Pointer(unsafe.StringData(string(v))))
	case starlark.Bytes:
		w.str(len(v), unsafe.Pointer(unsafe.StringData(string(v))))
	case starlark.Int:
		if _, ok := v.Int64(); !ok {
			w.n += headerBytes + int64(len(v.BigInt().Bits()))*8
		}
	case starlark.Float, starlark.Bool, starlark.NoneType:
	case starlark.Tuple:
		if len(v) == 0 || !w.first(unsafe.Pointer(&v[0])) {
			return
		}
		w.n += headerBytes + int64(len(v))*slotBytes
		for _, x := range v {
			w.push(x)
		}
	case *starlark.List:
		if !w.first(unsafe.Pointer(v)) {
			return
		}
		w.n += objectBytes + int64(v.Len())*slotBytes
		for i := 0; i < v.Len(); i++ {
			w.push(v.Index(i))
		}
	case *starlark.Dict:
		if !w.first(unsafe.Pointer(v)) {
			return
		}
		w.n += objectBytes + int64(v.Len())*entryBytes
		it := v.Iterate()
		var k starlark.Value
		for it.Next(&k) {
			w.push(k)
			if x, found, err := v.Get(k); err == nil && found {
				w.push(x)
			}
		}
		it.Done()
	case *starlark.Set:
		if !w.first(unsafe.Pointer(v)) {
			return
		}
		w.n += objectBytes + int64(v.Len())*entryBytes
		it := v.Iterate()
		var k starlark.Value
		for it.Next(&k) {
			w.push(k)
		}
		it.Done()
	case *starlark.Function:
		if !w.first(unsafe.Pointer(v)) {
			return
		}
		w.n += objectBytes
		for i := 0; i < v.NumParams(); i++ {
			w.push(v.ParamDefault(i))
		}
		for i := 0; i < v.NumFreeVars(); i++ {
			_, x := v.FreeVar(i)
			w.push(x)
		}
		if m := v.Module(); m != nil && !w.modules[m] {
			w.modules[m] = true
			for _, x := range v.Globals() {
				w.push(x)
			}
		}
	case *starlark.Builtin:
		// A bound method (l.append) holds its receiver.
		if !w.first(unsafe.Pointer(v)) {
			return
		}
		w.push(v.Receiver())
	case *starlarkstruct.Struct:
		if !w.first(unsafe.Pointer(v)) {
			return
		}
		d := starlark.StringDict{}
		v.ToStringDict(d)
		w.n += objectBytes + int64(len(d))*entryBytes
		for _, x := range d {
			w.push(x)
		}
	case *starlarkstruct.Module:
		if !w.first(unsafe.Pointer(v)) {
			return
		}
		w.n += objectBytes + int64(len(v.Members))*entryBytes
		for _, x := range v.Members {
			w.push(x)
		}
	default:
		// A captured variable is held in a cell (*starlark.cell, whose only
		// field is the value), which closures and their frames share.
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Pointer && rv.Type() == cellType {
			if p := rv.UnsafePointer(); w.first(p) {
				w.n += headerBytes
				w.push(*(*starlark.Value)(p))
			}
			return
		}
		w.n += headerBytes // a host value: its own memory is the host's
	}
}

// cellType is go.starlark.net's *cell when its layout is the one value
// reads (a pointer to a struct whose only field is a Value); nil otherwise,
// and cells then count as host values.
var cellType = findCell()

// findCell runs a closure and looks at the captured local's raw slot.
func findCell() reflect.Type {
	var found reflect.Type
	probe := starlark.NewBuiltin("probe", func(thread *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		fr := thread.DebugFrame(1)
		for i := 0; i < fr.NumLocals(); i++ {
			if b, v := fr.Local(i); b.Name == "captured" && v != nil {
				found = reflect.TypeOf(v)
			}
		}
		return starlark.None, nil
	})
	const src = "def f():\n    captured = 1\n    probe()\n    return lambda: captured\nf()\n"
	thread := &starlark.Thread{Name: "memlimit:probe"}
	if _, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, "probe.star", src, starlark.StringDict{"probe": probe}); err != nil || found == nil {
		return nil
	}
	if found.Kind() != reflect.Pointer || found.Elem().Kind() != reflect.Struct ||
		found.Elem().NumField() != 1 || found.Elem().Field(0).Type != valueType {
		return nil
	}
	return found
}

// frameSlots are a Starlark frame's locals followed by its operand stack:
// go.starlark.net allocates them as one slice, of which the frame keeps the
// locals. When the layout isn't the one expected, it is the locals alone.
func frameSlots(fr starlark.DebugFrame, fn *starlark.Function) []starlark.Value {
	rv := reflect.ValueOf(fr)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return locals(fr)
	}
	loc := rv.Elem().FieldByName("locals")
	code := reflect.ValueOf(fn).Elem().FieldByName("funcode")
	if !loc.IsValid() || loc.Kind() != reflect.Slice || loc.Type().Elem() != valueType ||
		!code.IsValid() || code.Kind() != reflect.Pointer || code.IsNil() {
		return locals(fr)
	}
	maxStack := code.Elem().FieldByName("MaxStack")
	if !maxStack.IsValid() || maxStack.Kind() != reflect.Int {
		return locals(fr)
	}
	p := loc.UnsafePointer()
	if p == nil {
		return nil // the frame of a call not yet entered
	}
	return unsafe.Slice((*starlark.Value)(p), loc.Len()+int(maxStack.Int()))
}

var valueType = reflect.TypeOf((*starlark.Value)(nil)).Elem()

func locals(fr starlark.DebugFrame) []starlark.Value {
	out := make([]starlark.Value, fr.NumLocals())
	for i := range out {
		_, out[i] = fr.Local(i)
	}
	return out
}
