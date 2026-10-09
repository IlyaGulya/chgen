// Package sqlir owns the parser-independent SELECT representation used by
// column binding and frontend coverage observations. Syntax structure alone
// does not carry inferred type or runtime proof.
package sqlir

type Document struct {
	Version int    `json:"version"`
	Select  Select `json:"select"`
}

type Select struct {
	With    []CTE      `json:"with,omitempty"`
	Items   []Item     `json:"items"`
	From    []Relation `json:"from,omitempty"`
	Where   *Expr      `json:"where,omitempty"`
	GroupBy []Expr     `json:"group_by,omitempty"`
	Having  *Expr      `json:"having,omitempty"`
	OrderBy []Order    `json:"order_by,omitempty"`
	Limit   *Expr      `json:"limit,omitempty"`
	Offset  *Expr      `json:"offset,omitempty"`
}

type CTE struct {
	Name  string `json:"name"`
	Query Select `json:"query"`
}

type Item struct {
	Expr  Expr   `json:"expr"`
	Alias string `json:"alias,omitempty"`
}

type Expr struct {
	Span  *Span    `json:"span,omitempty"`
	Kind  string   `json:"kind"`
	Name  []string `json:"name,omitempty"`
	Value string   `json:"value,omitempty"`
	Args  []Expr   `json:"args,omitempty"`
}

type Relation struct {
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
}
