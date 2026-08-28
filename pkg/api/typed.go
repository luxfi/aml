// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

// One shape for every operation, and two adapters onto it.
//
// An operation is `func(ctx, org, *In) (*Out, error)`: the tenant is an argument
// and never something the operation resolves for itself, the input is one typed
// struct, and the output is another. Every record plane below this package
// exposes exactly that, and this file is the whole of what turns one into an HTTP
// route.
//
// The point is that the CONTRACT is the pair of Go types and nothing else. A
// second face over the same planes — the zip typed operations in the cloud mount,
// where the same In and Out become the OpenAPI schema, the CLI, the SDKs and the
// MCP tools — wraps the same functions with no line of the contract restated. A
// handler that unpacked fields by hand would be that second copy, and the copy is
// what drifts.
//
// GET binds its input from the query string and POST from the body, both driven
// by the same json tags on the same struct. There is one binder for each and no
// third way.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/brand"
	"github.com/luxfi/aml/pkg/dictionary"
	"github.com/luxfi/aml/pkg/lists"
	"github.com/luxfi/aml/pkg/models"
	"github.com/luxfi/aml/pkg/suppress"
	"github.com/luxfi/aml/pkg/topology"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/watch"
)

// op is an operation over one tenant's records.
type op[In, Out any] func(ctx context.Context, org string, in *In) (*Out, error)

// maxBody bounds a request body. A list import is the largest legitimate one and
// it is bounded at a thousand values, so a megabyte is generous for every
// operation here and small enough that a body cannot be the attack.
const maxBody = 1 << 20

// post adapts an operation to a route that reads its input from the body.
func post[In, Out any](h *Handler, fn op[In, Out], created bool) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		who, err := h.caller(e)
		if err != nil {
			return refuse(e, err)
		}
		var in In
		body := http.MaxBytesReader(e.Response, e.Request.Body, maxBody)
		if err := json.NewDecoder(body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}
		// The path is bound last and wins. A body naming a different list than
		// the URL is a request whose meaning depends on which half the reader
		// believes, and the URL is the one the router matched on.
		if err := path(e.Request, &in); err != nil {
			return fail(e, http.StatusBadRequest, err.Error())
		}
		// And the decider is written after both, from the credential.
		decide(&in, who.Subject)
		out, err := fn(e.Request.Context(), who.Tenant, &in)
		if err != nil {
			return answer(e, err)
		}
		code := http.StatusOK
		if created {
			code = http.StatusCreated
		}
		return e.JSON(code, out)
	}
}

// get adapts an operation to a route that reads its input from the query string.
func get[In, Out any](h *Handler, fn op[In, Out]) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		who, err := h.caller(e)
		if err != nil {
			return refuse(e, err)
		}
		var in In
		if err := bind(e.Request.URL.Query(), &in); err != nil {
			return fail(e, http.StatusBadRequest, err.Error())
		}
		if err := path(e.Request, &in); err != nil {
			return fail(e, http.StatusBadRequest, err.Error())
		}
		decide(&in, who.Subject)
		out, err := fn(e.Request.Context(), who.Tenant, &in)
		if err != nil {
			return answer(e, err)
		}
		return e.JSON(http.StatusOK, out)
	}
}

// deciderType is the one type a caller may not fill.
var deciderType = reflect.TypeOf(types.Decider(""))

// decide writes the authenticated subject onto every decider field of a typed
// input, after the body and after the path.
//
// This is the whole of the binding, and it is the reason every plane's "reason
// and decider" refusal means something. The value is not merged with, defaulted
// from or preferred over anything the caller sent: fields of this type carry
// `json:"-"`, so nothing the caller sent ever reached one. An empty subject
// writes an empty decider, which every governed operation refuses — a decision
// that names nobody is refused rather than recorded.
func decide(target any, subject string) {
	v := reflect.ValueOf(target).Elem()
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Type == deciderType {
			v.Field(i).SetString(subject)
		}
	}
}

// bind fills a typed input from a query string, by the same json tags the body
// decoder reads.
//
// A parameter that cannot be read as its field's type is an ERROR and never a
// zero value. `?limit=all` silently becoming limit 0 becoming the default is how
// a caller asks for everything and is handed a page, and `?live=yes` silently
// becoming false is how a filter turns itself off.
func bind(q url.Values, target any) error {
	v := reflect.ValueOf(target).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		raw := strings.TrimSpace(q.Get(name))
		if raw == "" {
			continue
		}
		if err := set(v.Field(i), name, raw); err != nil {
			return err
		}
	}
	return nil
}

// path overlays the route's own parameters onto a typed input, by the same json
// tags. A field whose name the route does not carry as a parameter is untouched,
// so one binder serves every route and no route needs a list of which parameters
// it has.
func path(r *http.Request, target any) error {
	v := reflect.ValueOf(target).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		raw := strings.TrimSpace(r.PathValue(name))
		if raw == "" {
			continue
		}
		if err := set(v.Field(i), name, raw); err != nil {
			return err
		}
	}
	return nil
}

func set(f reflect.Value, name, raw string) error {
	if f.Type() == reflect.TypeOf(time.Time{}) {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return fmt.Errorf("%s: %q is not an RFC 3339 instant", name, raw)
		}
		f.Set(reflect.ValueOf(at.UTC()))
		return nil
	}
	switch f.Kind() {
	case reflect.String:
		f.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("%s: %q is not true or false", name, raw)
		}
		f.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("%s: %q is not a whole number", name, raw)
		}
		f.SetInt(n)
	case reflect.Float32, reflect.Float64:
		x, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("%s: %q is not a number", name, raw)
		}
		f.SetFloat(x)
	default:
		return fmt.Errorf("%s cannot be given in a query string", name)
	}
	return nil
}

// answer turns a refusal into a status.
//
// The table is here, at the one place the domain meets the transport, rather than
// as a status code carried on each error — an error that knows its HTTP status is
// an error that has learned about HTTP, and the planes below this package have
// no business knowing there is any.
//
// A refusal the table does not name is a fault and not a bad request: it is
// logged with its detail and answered generically, because an unrecognised error
// is the one whose text nobody has read for what it might disclose.
func answer(e *core.RequestEvent, err error) error {
	for _, known := range gone {
		if errors.Is(err, known) {
			return fail(e, http.StatusNotFound, err.Error())
		}
	}
	for _, known := range taken {
		if errors.Is(err, known) {
			return fail(e, http.StatusConflict, err.Error())
		}
	}
	for _, known := range busy {
		if errors.Is(err, known) {
			return fail(e, http.StatusTooManyRequests, err.Error())
		}
	}
	for _, known := range refused {
		if errors.Is(err, known) {
			return fail(e, http.StatusBadRequest, err.Error())
		}
	}
	for _, known := range broken {
		if errors.Is(err, known) {
			return unavailable(e, "record plane", err)
		}
	}
	if errors.Is(err, brand.ErrTenant) {
		// The identity resolved something that is not a tenant key. That is this
		// deployment's configuration and not the caller's request, so it is a
		// fault here and an unauthenticated request there.
		return refuse(e, err)
	}
	log.Printf("[aml] %s: %v", e.Request.URL.Path, err)
	return fail(e, http.StatusInternalServerError, "the request could not be completed")
}

// The refusal table. Every sentinel each plane publishes appears exactly once, so
// a plane that adds one and forgets it here answers 500 — loudly — rather than
// being quietly classified as a bad request.
var (
	gone = []error{
		lists.ErrNoList, lists.ErrNoEntry,
		suppress.ErrNotHere,
		watch.ErrNotHere,
		models.ErrNotHere, models.ErrNoFit,
	}
	taken = []error{
		lists.ErrExists, lists.ErrRetired,
		suppress.ErrLifted,
		watch.ErrRetired,
		models.ErrAdopted,
	}
	// A refusal that says "not now, and not because of anything you sent".
	busy    = []error{ErrBusy}
	refused = []error{
		lists.ErrName, lists.ErrKind, lists.ErrClass, lists.ErrValue, lists.ErrEmpty,
		lists.ErrDecider, lists.ErrReason, lists.ErrCrowded, lists.ErrMaxValues,
		suppress.ErrReason, suppress.ErrDecider, suppress.ErrBroad, suppress.ErrKind,
		suppress.ErrSubject, suppress.ErrWindow, suppress.ErrCrowded,
		watch.ErrRule, watch.ErrSubject, watch.ErrKind, watch.ErrAction, watch.ErrTo,
		watch.ErrCount, watch.ErrWithin, watch.ErrReason, watch.ErrDecider,
		models.ErrDecider, models.ErrShape, models.ErrNoModel, models.ErrNoHistory,
		topology.ErrEmptySpace, topology.ErrHuge, topology.ErrEmpty, topology.ErrShape,
		topology.ErrOrg, topology.ErrNoHistory,
		ErrTooLong,
	}
	broken = []error{
		lists.ErrStore, suppress.ErrStore, watch.ErrStore,
		models.ErrStore, dictionary.ErrStore,
	}
)
