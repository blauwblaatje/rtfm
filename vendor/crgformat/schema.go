// Package crgformat holds the CRG game data schemas (see README.md).
package crgformat

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var printer = message.NewPrinter(language.English)

//go:embed events.schema.json
var eventsSchemaJSON []byte

//go:embed game.schema.json
var gameSchemaJSON []byte

// Validators checks event log lines and game summaries against the schemas.
type Validators struct {
	events map[string]*jsonschema.Schema // one schema per event type
	clocks map[string][]string           // the clock fields ("pc", "jc") each type requires
	game   *jsonschema.Schema
}

// NewValidators compiles the embedded schemas.
//
// events.schema.json is one oneOf over all event types. Validating against
// the oneOf directly gives an error per non-matching branch (56 of them), so
// each branch is compiled as its own schema and an event is validated only
// against the branch for its "type".
func NewValidators() (*Validators, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(eventsSchemaJSON))
	if err != nil {
		return nil, fmt.Errorf("events.schema.json: %w", err)
	}
	root, _ := doc.(map[string]any)
	variants, _ := root["oneOf"].([]any)
	if len(variants) == 0 {
		return nil, fmt.Errorf("events.schema.json: no oneOf variants")
	}

	c := jsonschema.NewCompiler()
	v := &Validators{events: map[string]*jsonschema.Schema{}, clocks: map[string][]string{}}
	for _, raw := range variants {
		variant := raw.(map[string]any)
		name, _ := variant["title"].(string)
		doc := map[string]any{"$schema": root["$schema"], "$defs": root["$defs"]}
		for k, val := range variant {
			doc[k] = val
		}
		url := "mem:///events/" + name + ".json"
		if err := c.AddResource(url, doc); err != nil {
			return nil, fmt.Errorf("event schema %s: %w", name, err)
		}
		sch, err := c.Compile(url)
		if err != nil {
			return nil, fmt.Errorf("event schema %s: %w", name, err)
		}
		v.events[name] = sch
		req, _ := variant["required"].([]any)
		for _, r := range req {
			if r == "pc" || r == "jc" {
				v.clocks[name] = append(v.clocks[name], r.(string))
			}
		}
	}

	gameDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(gameSchemaJSON))
	if err != nil {
		return nil, fmt.Errorf("game.schema.json: %w", err)
	}
	if err := c.AddResource("mem:///game.json", gameDoc); err != nil {
		return nil, fmt.Errorf("game.schema.json: %w", err)
	}
	if v.game, err = c.Compile("mem:///game.json"); err != nil {
		return nil, fmt.Errorf("game.schema.json: %w", err)
	}
	return v, nil
}

// RequiredClocks returns the clock fields ("pc", "jc") an event type must carry.
func (v *Validators) RequiredClocks(typ string) []string { return v.clocks[typ] }

// KnownEvent reports whether typ is a known event type.
func (v *Validators) KnownEvent(typ string) bool { return v.events[typ] != nil }

// ValidateEvent checks one decoded log line (as produced by DecodeJSON).
func (v *Validators) ValidateEvent(line any) error {
	obj, ok := line.(map[string]any)
	if !ok {
		return fmt.Errorf("not a JSON object")
	}
	typ, _ := obj["type"].(string)
	sch := v.events[typ]
	if sch == nil {
		return fmt.Errorf("unknown event type %q", typ)
	}
	return flatten(sch.Validate(line))
}

// ValidateGame checks a decoded game summary.
func (v *Validators) ValidateGame(doc any) error { return flatten(v.game.Validate(doc)) }

// DecodeJSON decodes JSON the way the validator expects (numbers as json.Number).
func DecodeJSON(data []byte) (any, error) { return jsonschema.UnmarshalJSON(bytes.NewReader(data)) }

// flatten turns the validator's error tree into one line per failing leaf.
func flatten(err error) error {
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return err
	}
	var msgs []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := "/" + strings.Join(e.InstanceLocation, "/")
			msgs = append(msgs, fmt.Sprintf("at %s: %s", loc, e.ErrorKind.LocalizedString(printer)))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}
