// Package engine implements typed Go wrapper generation for annotated ClickHouse SQL.
//
// ParseSchemaCatalogs, ParseQueryFiles, and Generate provide the generation
// pipeline for packages that need direct control of each step.
//
// InferExpressionType is for conformance tools. It checks one expression
// against one fixture schema. MeasuredCHVersion identifies the ClickHouse
// version used to verify the type rules.
//
// The package returns an error when it cannot prove a type. An explicit type
// annotation can supply a type for an unsupported expression.
package engine
