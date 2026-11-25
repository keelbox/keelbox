package hetzner

import (
	"errors"
	"fmt"
	"sort"
)

// Hetzner can decline to create a server, or to change one's type, for a
// reason of its own: the type is out of stock in the location, or the
// account may not order there for now. Since June 2026 Hetzner limits new
// servers per location for new customers and some existing ones, citing
// hardware; affected accounts can neither create nor rescale servers there
// until the limit lifts by itself. The API's code for that is not
// documented, so any 403, 412 or 422 that is not about keelbox's own
// request counts (docs/adr/0095-hetzner-refusals.md).

// RefusedError is Hetzner declining an order: what was refused, Hetzner's
// own answer, and what keelbox did about what it had made for it.
type RefusedError struct {
	// API is Hetzner's answer. It is nil where Hetzner's list of server
	// types already says the type cannot be ordered in the location, and
	// keelbox sent no order.
	API *APIError
	// Action is what was refused: "create", "resume" or "resize".
	Action string
	Box    string
	// Location and ServerType are what was asked for.
	Location, ServerType string
	// Server is the id of the server a resize was for.
	Server int64
	// Types is Hetzner's list of server types when it refused, for the
	// alternatives.
	Types []ServerType
	// OtherTypes are the other types that can be ordered in Location now
	// and would do (for a resume: that fit the snapshot), cheapest first;
	// OtherLocations are the other locations where ServerType can be.
	// Neither is set for a resize, whose sizes keelbox resize lists.
	OtherTypes, OtherLocations []string
	// Left says what is left of the attempt, as a sentence: "No server
	// was created, ...". Empty where nothing needs saying.
	Left string
}

func (e *RefusedError) Error() string {
	var s string
	switch {
	case e.API == nil:
		s = fmt.Sprintf("Hetzner lists server type %s as not orderable in %s now", e.ServerType, e.Location)
	case e.Action == "resize":
		s = fmt.Sprintf("Hetzner refused to change server %d to %s (%v). Hetzner may be limiting changes to servers for this account or in %s, and lifts such limits by itself (status.hetzner.com)",
			e.Server, e.ServerType, e.API, e.Location)
	case e.Action == "resume":
		s = fmt.Sprintf("Hetzner refused to create %s's server again from its snapshot, %s in %s (%v). Hetzner may be limiting new servers for this account or in %s, and lifts such limits by itself (status.hetzner.com)",
			e.Box, e.ServerType, e.Location, e.API, e.Location)
	default:
		s = fmt.Sprintf("Hetzner refused to create %s's server, %s in %s (%v). Hetzner may be limiting new servers for this account or in %s, and lifts such limits by itself (status.hetzner.com)",
			e.Box, e.ServerType, e.Location, e.API, e.Location)
	}
	if e.Left != "" {
		s += ". " + e.Left
	}
	return s
}

func (e *RefusedError) Unwrap() error {
	if e.API == nil {
		return nil
	}
	return e.API
}

// notRefusals are the API's codes for something wrong with keelbox's own
// request or token, which no other location or type would change.
var notRefusals = map[string]bool{
	"invalid_input": true, "json_error": true, "unauthorized": true, //nolint:misspell // the API's error code
	"token_readonly":   true,
	"uniqueness_error": true, "not_found": true, "locked": true, "protected": true, "conflict": true,
	"resource_in_use": true, "server_not_stopped": true, "rate_limit_exceeded": true,
}

// refusal reports whether err is Hetzner declining an order for a reason of
// its own: resource_unavailable, resource_limit_exceeded, placement_error
// or forbidden, or any other 403, 412 or 422 that is not about the request.
func refusal(err error) (*APIError, bool) {
	var ae *APIError
	if !errors.As(err, &ae) {
		return nil, false
	}
	switch ae.Code {
	case "resource_unavailable", "resource_limit_exceeded", "placement_error", "forbidden":
		return ae, true
	}
	if notRefusals[ae.Code] {
		return nil, false
	}
	switch ae.Status {
	case 403, 412, 422:
		return ae, true
	}
	return nil, false
}

// alternatives fills in OtherTypes and OtherLocations from Types; fits
// says which types would do, nil for any.
func (e *RefusedError) alternatives(fits func(ServerType) bool) *RefusedError {
	for _, o := range Offers(e.Types, e.Location) {
		if o.Type.Name != e.ServerType && (fits == nil || fits(o.Type)) {
			e.OtherTypes = append(e.OtherTypes, o.Type.Name)
		}
	}
	e.OtherLocations = OrderableElsewhere(e.Types, e.ServerType, e.Location)
	return e
}

// OrderableElsewhere lists, by name, the locations other than location
// where the type can be ordered now.
func OrderableElsewhere(types []ServerType, serverType, location string) []string {
	var out []string
	for _, t := range types {
		if t.Name != serverType || t.Deprecated {
			continue
		}
		for _, l := range t.OrderableIn() {
			if l != location && !t.deprecatedIn(l) {
				out = append(out, l)
			}
		}
	}
	sort.Strings(out)
	return out
}

// OfferLocations lists, by name, every location where some type can be
// ordered now.
func OfferLocations(types []ServerType) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range types {
		for _, p := range t.Prices {
			if !seen[p.Location] && len(Offers(types, p.Location)) > 0 {
				seen[p.Location] = true
				out = append(out, p.Location)
			}
		}
	}
	sort.Strings(out)
	return out
}
