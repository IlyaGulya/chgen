// Package sqlir owns the parser-independent SELECT representation used by
// frontend coverage observations. It carries syntax structure, not inferred type proof.
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
	Kind  string   `json:"kind"`
	Name  []string `json:"name,omitempty"`
	Value string   `json:"value,omitempty"`
	Args  []Expr   `json:"args,omitempty"`
}

type Relation struct {
	Kind  string   `json:"kind"`
	Name  []string `json:"name,omitempty"`
	Call  *Expr    `json:"call,omitempty"`
	Alias string   `json:"alias,omitempty"`
}

type Order struct {
	Expr      Expr   `json:"expr"`
	Direction string `json:"direction"`
}
