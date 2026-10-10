// Package sqlir owns the parser-independent SELECT representation used by
// column binding and frontend coverage observations. Syntax structure alone
// does not carry inferred type or runtime proof.
package sqlir

type Document struct {
	Version int    `json:"version"`
	Select  Select `json:"select"`
}

type Select struct {
	Distinct      bool               `json:"distinct,omitzero"`
	DistinctOn    []Expr             `json:"distinct_on,omitempty"`
	Prewhere      *Expr              `json:"prewhere,omitempty"`
	Top           *Top               `json:"top,omitempty"`
	LimitBy       *LimitBy           `json:"limit_by,omitempty"`
	Settings      []Setting          `json:"settings,omitempty"`
	Format        string             `json:"format,omitempty"`
	Inner         *Select            `json:"inner,omitempty"`
	SetOperations []SetOperation     `json:"set_operations,omitempty"`
	With          []CTE              `json:"with,omitempty"`
	Items         []Item             `json:"items"`
	From          []Relation         `json:"from,omitempty"`
	Where         *Expr              `json:"where,omitempty"`
	GroupBy       []Expr             `json:"group_by,omitempty"`
	Having        *Expr              `json:"having,omitempty"`
	OrderBy       []Order            `json:"order_by,omitempty"`
	Limit         *Expr              `json:"limit,omitempty"`
	LimitWithTies bool               `json:"limit_with_ties,omitzero"`
	Offset        *Expr              `json:"offset,omitempty"`
	Windows       []WindowDefinition `json:"windows,omitempty"`
}

type Top struct {
	Count    Expr `json:"count"`
	WithTies bool `json:"with_ties,omitzero"`
}

type LimitBy struct {
	Count  Expr   `json:"count"`
	Offset *Expr  `json:"offset,omitempty"`
	Keys   []Expr `json:"keys"`
}

type Setting struct {
	Name  string `json:"name"`
	Value Expr   `json:"value"`
}

type SetOperation struct {
	Kind  string `json:"kind"`
	Query Select `json:"query"`
}

type WindowDefinition struct {
	Name string `json:"name"`
	Spec Window `json:"spec"`
}

type CTE struct {
	Name  string `json:"name"`
	Query Select `json:"query,omitzero"`
	Expr  *Expr  `json:"expr,omitempty"`
}

type Item struct {
	Expr  Expr   `json:"expr"`
	Alias string `json:"alias,omitempty"`
}

type Expr struct {
	Span          *Span    `json:"span,omitempty"`
	Kind          string   `json:"kind"`
	Name          []string `json:"name,omitempty"`
	Value         string   `json:"value,omitempty"`
	Base          int      `json:"base,omitempty"`
	Type          string   `json:"type,omitempty"`
	Args          []Expr   `json:"args,omitempty"`
	Parameters    []Expr   `json:"parameters,omitempty"`
	Distinct      bool     `json:"distinct,omitzero"`
	Query         *Select  `json:"query,omitempty"`
	Parenthesized bool     `json:"parenthesized,omitzero"`
	Quoted        bool     `json:"quoted,omitzero"`
	Window        *Window  `json:"window,omitempty"`
}

type Window struct {
	Span          *Span   `json:"span,omitempty"`
	Base          string  `json:"base,omitempty"`
	PartitionBy   []Expr  `json:"partition_by,omitempty"`
	OrderBy       []Order `json:"order_by,omitempty"`
	Frame         *Expr   `json:"frame,omitempty"`
	Parenthesized bool    `json:"parenthesized,omitzero"`
}

type Relation struct {
	Final         bool      `json:"final,omitzero"`
	Span          *Span     `json:"span,omitempty"`
	Kind          string    `json:"kind"`
	Name          []string  `json:"name,omitempty"`
	Call          *Expr     `json:"call,omitempty"`
	Alias         string    `json:"alias,omitempty"`
	Query         *Select   `json:"query,omitempty"`
	Left          *Relation `json:"left,omitempty"`
	Right         *Relation `json:"right,omitempty"`
	Modifiers     []string  `json:"modifiers,omitempty"`
	On            []Expr    `json:"on,omitempty"`
	Using         []Expr    `json:"using,omitempty"`
	Items         []Item    `json:"items,omitempty"`
	Parenthesized bool      `json:"parenthesized,omitzero"`
}

// Span is a half-open range of UTF-8 byte offsets in the parsed SQL.
// Source provenance is not inferred from the spelling of an expression.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type Order struct {
	Expr      Expr   `json:"expr"`
	Direction string `json:"direction"`
	Fill      *Fill  `json:"fill,omitempty"`
}

type Fill struct {
	From      *Expr `json:"from,omitempty"`
	To        *Expr `json:"to,omitempty"`
	Step      *Expr `json:"step,omitempty"`
	Staleness *Expr `json:"staleness,omitempty"`
}
