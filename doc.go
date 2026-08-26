// Package chgen generates typed Go wrappers for annotated ClickHouse SQL.
//
// Run reads a chgen configuration file and generates all declared packages.
// LoadConfig reads the configuration without code generation. ParseSchemaCatalogs,
// ParseQueryFiles, and Generate provide the same pipeline for programs that need
// direct control of each step.
//
// InferExpressionType is for conformance tools. It checks one expression against
// one fixture schema. MeasuredCHVersion identifies the ClickHouse version used to
// verify the type rules.
//
// The package returns an error when it cannot prove a type. An explicit type
// annotation can supply a type for an unsupported expression.
package chgen
