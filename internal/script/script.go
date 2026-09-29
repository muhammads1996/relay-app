// Package script runs pre-request and test scripts via an embedded goja
// (ES2017) runtime with a pm.* shim compatible with the common Postman subset.
// Scripts are sandboxed: no filesystem or network access. CPU and wall-clock
// time are capped per execution.
package script

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// TestResult is the outcome of one pm.test(...) call.
type TestResult struct {
	Name   string
	Passed bool
	Error  string
}

// ConsoleMessage is diagnostic output from a script and does not affect its result.
type ConsoleMessage struct {
	Level   string
	Message string
}

// Response is the read-only HTTP result available as pm.response inside scripts.
type Response struct {
	Code       int
	Status     string // e.g. "200 OK"
	Headers    map[string]string
	Body       []byte
	DurationMs float64
}

// Scope is the variable storage bridged into the script runtime.
// Scripts read/write through this; the caller decides which changes to keep.
type Scope struct {
	Env        map[string]string // pm.environment
	Collection map[string]string // pm.collectionVariables
	Request    map[string]string // pm.variables (request-scoped, read merged view)
}

// RunResult is the aggregate output of executing one script phase.
type RunResult struct {
	Tests   []TestResult
	Console []ConsoleMessage
	Errors  []string // uncaught script/runtime errors only
	// UpdatedVars contains variable mutations written by the script (merged
	// layer: env > collection) for the caller to propagate.
	UpdatedVars map[string]string
}

// Timeout caps how long a single script phase may run.
const Timeout = 5 * time.Second

// RunPreRequest executes a pre-request script. It may mutate scope variables
// (pm.environment.set, pm.collectionVariables.set) but the response is not
// yet available, so pm.response is nil. Returns any console/throw errors.
func RunPreRequest(src string, scope *Scope) *RunResult {
	return run(src, scope, nil)
}

// RunTests executes a post-response test script. pm.test calls are collected
// into RunResult.Tests. Throws and runtime errors land in RunResult.Errors.
func RunTests(src string, scope *Scope, resp *Response) *RunResult {
	return run(src, scope, resp)
}

func run(src string, scope *Scope, resp *Response) *RunResult {
	if strings.TrimSpace(src) == "" {
		return &RunResult{}
	}
	vm := goja.New()
	res := &RunResult{UpdatedVars: map[string]string{}}

	// Interrupt after timeout.
	timer := time.AfterFunc(Timeout, func() { vm.Interrupt("script timeout") })
	defer timer.Stop()

	installConsole(vm, res)
	installPM(vm, scope, resp, res)

	_, err := vm.RunString(src)
	if err != nil {
		msg := err.Error()
		// interrupt errors are expected on timeout; surface as a test failure.
		if strings.Contains(msg, "script timeout") {
			res.Errors = append(res.Errors, "script timed out after "+Timeout.String())
		} else {
			res.Errors = append(res.Errors, msg)
		}
	}
	return res
}

// installConsole wires console.log/warn/error to an ordered diagnostic stream.
func installConsole(vm *goja.Runtime, res *RunResult) {
	con := vm.NewObject()
	log := func(level string) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			parts := make([]string, len(call.Arguments))
			for i, a := range call.Arguments {
				parts[i] = consoleValue(a)
			}
			res.Console = append(res.Console, ConsoleMessage{Level: level, Message: strings.Join(parts, " ")})
			return goja.Undefined()
		}
	}
	_ = con.Set("log", log("log"))
	_ = con.Set("warn", log("warn"))
	_ = con.Set("error", log("error"))
	_ = vm.Set("console", con)
}

func consoleValue(value goja.Value) string {
	if goja.IsUndefined(value) {
		return "undefined"
	}
	if goja.IsNull(value) {
		return "null"
	}
	if obj, ok := value.(*goja.Object); ok {
		if raw, err := json.Marshal(obj.Export()); err == nil {
			return string(raw)
		}
	}
	return fmt.Sprint(value)
}

// installPM wires the pm.* shim into the VM.
func installPM(vm *goja.Runtime, scope *Scope, resp *Response, res *RunResult) {
	pm := vm.NewObject()

	// ---- pm.test ----
	_ = pm.Set("test", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 2 {
			return goja.Undefined()
		}
		name := call.Arguments[0].String()
		tr := TestResult{Name: name}
		fn, ok := goja.AssertFunction(call.Arguments[1])
		if !ok {
			tr.Error = "second argument must be a function"
			res.Tests = append(res.Tests, tr)
			return goja.Undefined()
		}
		defer func() {
			if r := recover(); r != nil {
				tr.Passed = false
				tr.Error = fmt.Sprintf("%v", r)
				res.Tests = append(res.Tests, tr)
			}
		}()
		_, err := fn(goja.Undefined())
		if err != nil {
			tr.Passed = false
			tr.Error = userScriptError(err)
		} else {
			tr.Passed = true
		}
		res.Tests = append(res.Tests, tr)
		return goja.Undefined()
	})

	// ---- pm.expect — Chai-style ----
	_ = pm.Set("expect", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		val := call.Arguments[0]
		return newExpect(vm, val)
	})

	// ---- pm.environment ----
	envObj := scopeAccessor(vm, scope.Env, res.UpdatedVars)
	_ = pm.Set("environment", envObj)

	// ---- pm.collectionVariables ----
	colObj := scopeAccessor(vm, scope.Collection, res.UpdatedVars)
	_ = pm.Set("collectionVariables", colObj)

	// ---- pm.variables (read-only merged view) ----
	merged := map[string]string{}
	for k, v := range scope.Collection {
		merged[k] = v
	}
	for k, v := range scope.Env {
		merged[k] = v
	}
	for k, v := range scope.Request {
		merged[k] = v
	}
	varsObj := scopeAccessor(vm, merged, nil)
	_ = pm.Set("variables", varsObj)

	// ---- pm.response ----
	if resp != nil {
		_ = pm.Set("response", buildResponse(vm, resp))
	} else {
		_ = pm.Set("response", goja.Null())
	}

	// ---- pm.request (stub: available in pre-request) ----
	reqObj := vm.NewObject()
	_ = reqObj.Set("headers", vm.NewObject())
	_ = pm.Set("request", reqObj)

	_ = vm.Set("pm", pm)
}

// userScriptError removes Goja's internal stack frame from normal assertion
// failures. The assertion message is useful to API-client users; a Go method
// path such as script.newExpect.func2 is not.
func userScriptError(err error) string {
	var ex *goja.Exception
	if errors.As(err, &ex) && ex.Value() != nil {
		return ex.Value().String()
	}
	return err.Error()
}

func scopeAccessor(vm *goja.Runtime, store map[string]string, updates map[string]string) *goja.Object {
	obj := vm.NewObject()
	_ = obj.Set("get", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		k := call.Arguments[0].String()
		if v, ok := store[k]; ok {
			return vm.ToValue(v)
		}
		return goja.Undefined()
	})
	_ = obj.Set("set", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 2 {
			return goja.Undefined()
		}
		k := call.Arguments[0].String()
		v := call.Arguments[1].String()
		store[k] = v
		if updates != nil {
			updates[k] = v
		}
		return goja.Undefined()
	})
	_ = obj.Set("unset", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		k := call.Arguments[0].String()
		delete(store, k)
		if updates != nil {
			delete(updates, k)
		}
		return goja.Undefined()
	})
	_ = obj.Set("has", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return vm.ToValue(false)
		}
		_, ok := store[call.Arguments[0].String()]
		return vm.ToValue(ok)
	})
	return obj
}

func buildResponse(vm *goja.Runtime, resp *Response) *goja.Object {
	obj := vm.NewObject()
	_ = obj.Set("code", resp.Code)
	_ = obj.Set("status", resp.Status)
	_ = obj.Set("responseTime", resp.DurationMs)

	// pm.response.json() — parses body as JSON
	_ = obj.Set("json", func(call goja.FunctionCall) goja.Value {
		var v any
		if err := json.Unmarshal(resp.Body, &v); err != nil {
			panic(vm.ToValue("response body is not JSON: " + err.Error()))
		}
		parsed, _ := vm.RunString("(" + string(resp.Body) + ")")
		return parsed
	})

	// pm.response.text()
	_ = obj.Set("text", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(string(resp.Body))
	})

	// pm.response.headers
	hObj := vm.NewObject()
	_ = hObj.Set("get", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		k := strings.ToLower(call.Arguments[0].String())
		for hk, hv := range resp.Headers {
			if strings.ToLower(hk) == k {
				return vm.ToValue(hv)
			}
		}
		return goja.Undefined()
	})
	_ = obj.Set("headers", hObj)

	// pm.response.to — convenience assertion bridge
	toObj := vm.NewObject()
	_ = toObj.Set("have", vm.NewObject())
	statusObj := vm.NewObject()
	_ = statusObj.Set("status", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		want := int(call.Arguments[0].ToInteger())
		if resp.Code != want {
			panic(vm.ToValue(fmt.Sprintf("expected status %d but got %d", want, resp.Code)))
		}
		return goja.Undefined()
	})
	_ = toObj.Set("have", statusObj)
	_ = obj.Set("to", toObj)

	return obj
}

// newExpect returns a chainable Chai-style expect object for the given value.
func newExpect(vm *goja.Runtime, val goja.Value) goja.Value {
	obj := vm.NewObject()

	fail := func(msg string) {
		panic(vm.ToValue(msg))
	}

	// .to / .be / .and / .have / .is — chainable no-ops that return self
	for _, prop := range []string{"to", "be", "and", "have", "is", "not", "that", "which", "with", "at", "a", "an"} {
		_ = obj.Set(prop, obj)
	}

	// .equal(v) / .eql(v)
	eqlFn := func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return obj
		}
		want := call.Arguments[0].Export()
		got := val.Export()
		if !deepEqual(got, want) {
			fail(fmt.Sprintf("expected %v to equal %v", got, want))
		}
		return obj
	}
	_ = obj.Set("equal", eqlFn)
	_ = obj.Set("eql", eqlFn)
	_ = obj.Set("equals", eqlFn)

	// .include(v) / .contain(v)
	includeFn := func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return obj
		}
		needle := call.Arguments[0].String()
		haystack := val.String()
		if !strings.Contains(haystack, needle) {
			fail(fmt.Sprintf("expected %q to include %q", haystack, needle))
		}
		return obj
	}
	_ = obj.Set("include", includeFn)
	_ = obj.Set("contain", includeFn)

	// .below(n)
	_ = obj.Set("below", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return obj
		}
		limit := call.Arguments[0].ToFloat()
		actual := val.ToFloat()
		if actual >= limit {
			fail(fmt.Sprintf("expected %v to be below %v", actual, limit))
		}
		return obj
	})

	// .above(n)
	_ = obj.Set("above", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return obj
		}
		limit := call.Arguments[0].ToFloat()
		actual := val.ToFloat()
		if actual <= limit {
			fail(fmt.Sprintf("expected %v to be above %v", actual, limit))
		}
		return obj
	})

	// .ok — truthy check
	_ = obj.Set("ok", func(call goja.FunctionCall) goja.Value {
		if !val.ToBoolean() {
			fail(fmt.Sprintf("expected %v to be truthy", val.Export()))
		}
		return obj
	})

	// .true / .false
	_ = obj.Set("true", func(call goja.FunctionCall) goja.Value {
		if !val.ToBoolean() {
			fail(fmt.Sprintf("expected true but got %v", val.Export()))
		}
		return obj
	})
	_ = obj.Set("false", func(call goja.FunctionCall) goja.Value {
		if val.ToBoolean() {
			fail(fmt.Sprintf("expected false but got %v", val.Export()))
		}
		return obj
	})

	// .property(name) — navigate into an object
	_ = obj.Set("property", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return obj
		}
		key := call.Arguments[0].String()
		o, ok := val.(*goja.Object)
		if !ok {
			fail(fmt.Sprintf("expected an object but got %T", val.Export()))
		}
		child := o.Get(key)
		if child == nil || goja.IsUndefined(child) || goja.IsNull(child) {
			fail(fmt.Sprintf("expected object to have property %q", key))
		}
		return newExpect(vm, child)
	})

	// .lengthOf(n)
	_ = obj.Set("lengthOf", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return obj
		}
		want := int(call.Arguments[0].ToInteger())
		if o, ok := val.(*goja.Object); ok {
			lenVal := o.Get("length")
			if lenVal != nil {
				got := int(lenVal.ToInteger())
				if got != want {
					fail(fmt.Sprintf("expected length %d but got %d", want, got))
				}
				return obj
			}
		}
		s := val.String()
		if len(s) != want {
			fail(fmt.Sprintf("expected length %d but got %d", want, len(s)))
		}
		return obj
	})

	return obj
}

func deepEqual(a, b any) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}
