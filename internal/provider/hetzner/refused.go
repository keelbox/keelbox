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
// hardware; affected accounts can neither create nor rescale servers
// there. The API's code for that is not documented, so any 403, 412 or
// 422 that is not about keelbox's own request counts. A project at one of
// its limits (resource_limit_exceeded) is refused everywhere, until the
// limit is raised or a server deleted (docs/adr/0095-hetzner-refusals.md).

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
	why := fmt.Sprintf("Hetzner may be limiting new servers for this account or in %s (status.hetzner.com)", e.Location)
	if e.Action == "resize" {
		why = fmt.Sprintf("Hetzner may be limiting changes to servers for this account or in %s (status.hetzner.com)", e.Location)
	}
	if e.Quota() {
		why = "The project is at one of its Hetzner limits, such as its number of servers or cores, in every location. Ask Hetzner for a higher limit in the Hetzner Console, or delete a server you no longer need"
	}
	switch {
	case e.API == nil:
		s = fmt.Sprintf("Hetzner lists server type %s as not orderable in %s now", e.ServerType, e.Location)
	case e.Action == "resize":
		s = fmt.Sprintf("Hetzner refused to change server %d to %s (%v). %s", e.Server, e.ServerType, e.API, why)
	case e.Action == "resume":
		s = fmt.Sprintf("Hetzner refused to create %s's server again from its snapshot, %s in %s (%v). %s", e.Box, e.ServerType, e.Location, e.API, why)
	default:
		s = fmt.Sprintf("Hetzner refused to create %s's server, %s in %s (%v). %s", e.Box, e.ServerType, e.Location, e.API, why)
	}
	if e.Left != "" {
		s += ". " + e.Left
	}
	return s
}

// NewRefusedError is Hetzner's answer api to an order for box, as Create
// returns it, with the alternatives from types filled in.
func NewRefusedError(api *APIError, box, location, serverType string, types []ServerType, left string) *RefusedError {
	return (&RefusedError{API: api, Action: "create", Box: box, Location: location, ServerType: serverType, Types: types, Left: left}).alternatives(nil)
}

// Quota reports whether the project is at one of its limits: no other
// location or size changes that, and it does not pass by itself.
func (e *RefusedError) Quota() bool { return e.API != nil && e.API.Code == "resource_limit_exceeded" }

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
	"unsupported_error": true, "invalid_server_type": true,
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
// says which types would do, nil for any. The types are the nearest to
// the one asked for first (NearestOffers), of its architecture only.
func (e *RefusedError) alternatives(fits func(ServerType) bool) *RefusedError {
	like, ok := TypeNamed(e.Types, e.ServerType)
	for _, o := range NearestOffers(Offers(e.Types, e.Location), like) {
		if o.Type.Name == e.ServerType || (fits != nil && !fits(o.Type)) || (ok && archFor(o.Type) != archFor(like)) {
			continue
		}
		e.OtherTypes = append(e.OtherTypes, o.Type.Name)
	}
	e.OtherLocations = OrderableElsewhere(e.Types, e.ServerType, e.Location)
	return e
}

// TypeNamed is the type of that name in the list, and whether there is
// one.
func TypeNamed(types []ServerType, name string) (ServerType, bool) {
	for _, t := range types {
		if t.Name == name {
			return t, true
		}
	}
	return ServerType{}, false
}

// NearestOffers orders offers by how near each is to the type like: of
// its architecture with at least its cores and memory, in the offers' own
// order (cheapest first); then the rest of its architecture, the largest
// first; then the other architecture, in the offers' order. like's zero
// value keeps them as they are.
func NearestOffers(offers []Offer, like ServerType) []Offer {
	if like.Name == "" {
		return offers
	}
	rank := func(o Offer) int {
		switch {
		case archFor(o.Type) != archFor(like):
			return 2
		case o.Type.Cores >= like.Cores && o.Type.Memory >= like.Memory:
			return 0
		}
		return 1
	}
	out := append([]Offer(nil), offers...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := rank(a), rank(b); ra != rb || ra != 1 {
			return ra < rb
		}
		if a.Type.Memory != b.Type.Memory {
			return a.Type.Memory > b.Type.Memory
		}
		return a.Type.Cores > b.Type.Cores
	})
	return out
}

// NearestOffer is the index in offers of the offer nearest to like, or of
// DefaultOffer's pick when like is not known.
func NearestOffer(offers []Offer, like ServerType) int {
	if like.Name == "" || len(offers) == 0 {
		return DefaultOffer(offers)
	}
	best := NearestOffers(offers, like)[0]
	for i, o := range offers {
		if o.Type.Name == best.Type.Name {
			return i
		}
	}
	return DefaultOffer(offers)
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
