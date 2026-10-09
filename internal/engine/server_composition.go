package engine

import (
	"fmt"
	"slices"
)

// Server composition shares the finite options and parameter-ownership model
// with offline composition. Every concrete SELECT still receives its own
// lexical validation and server result contract before any file is written.
func prepareServerComposition(query Query, declarations []ServerParameter, tables []TableChoice, external *Schema) (Query, error) {
	declarations, err := serverLegacyDeclarations(query.SQL, declarations, query.serverParamAnnotations)
	if err != nil {
		return Query{}, err
	}
	sql, err := lowerServerArguments(query.SQL, declarations)
	if err != nil {
		return Query{}, err
	}
	parts, options, err := parseConditionalSQL(sql)
	if err != nil {
		return Query{}, compositionError(query, err)
	}
	count, err := compositionVariantCount(options, tables)
	if err != nil {
		return Query{}, compositionError(query, err)
	}
	composition := &QueryComposition{Options: options, Tables: tables, Owners: make(map[string]string)}
	for _, choice := range tables {
		for _, table := range choice.Tables {
			if external != nil {
				if _, exists := external.Tables[table]; exists {
					return Query{}, compositionError(query, fmt.Errorf("table choice %s cannot select external schema %s", choice.Name, table))
				}
			}
		}
	}
	allParams := make(map[string]CHType)
	for mask := range count {
		variant := query
		body, used, err := replaceTableChoices(compositionSQL(parts, options, mask), selectedTables(composition, mask))
		if err != nil {
			return Query{}, compositionError(query, err)
		}
		for _, choice := range tables {
			if !used[choice.Name] {
				return Query{}, compositionError(query, fmt.Errorf("table choice %s is unused in variant %d", choice.Name, mask+1))
			}
		}
		variant.SQL, variant.ExternalParams, err = normalizeExternalTables(body, external, nil)
		if err != nil {
			return Query{}, err
		}
		_, parameters, err := PrepareServerSelect(variant.SQL)
		if err != nil {
			return Query{}, fmt.Errorf("composition variant %d: %w", mask+1, err)
		}
		for _, param := range parameters {
			if previous, exists := allParams[param.Name]; exists && previous.String() != param.Type.String() {
				return Query{}, fmt.Errorf("conflicting type for parameter %s", param.Name)
			}
			allParams[param.Name] = param.Type
		}
		variant.serverParamAnnotations = slices.DeleteFunc(slices.Clone(query.serverParamAnnotations), func(annotation Param) bool {
			return !slices.ContainsFunc(parameters, func(param ServerParameter) bool { return serverResultGoName(param.Name) == annotation.GoName })
		})
		composition.Variants = append(composition.Variants, variant)
	}
	for _, declaration := range declarations {
		typ, exists := allParams[declaration.Name]
		if !exists {
			return Query{}, fmt.Errorf("unused parameter declaration %s", declaration.Name)
		}
		if typ.String() != declaration.Type.String() {
			return Query{}, fmt.Errorf("conflicting type for parameter %s", declaration.Name)
		}
	}
	for _, annotation := range query.serverParamAnnotations {
		if !slices.ContainsFunc(composition.Variants, func(variant Query) bool {
			return slices.ContainsFunc(variant.serverParamAnnotations, func(param Param) bool { return param.GoName == annotation.GoName })
		}) {
			return Query{}, fmt.Errorf("unused parameter annotation %s", annotation.GoName)
		}
	}
	if len(options) == 0 && len(tables) == 0 {
		return composition.Variants[0], nil
	}
	query.Composition = composition
	return query, nil
}

// WithServerCompositionResults installs the common result contract only after
// every concrete variant was described and all shared types were checked.
func WithServerCompositionResults(input Query, variants []Query) (Query, error) {
	if input.Composition == nil || len(variants) != len(input.Composition.Variants) {
		return Query{}, fmt.Errorf("incomplete server composition contract")
	}
	for i := range variants {
		if err := validateServerQuery(variants[i]); err != nil {
			return Query{}, err
		}
		if variants[i].serverVersion != variants[0].serverVersion {
			return Query{}, fmt.Errorf("server version changed during composition analysis")
		}
		_, params, err := PrepareServerSelect(variants[i].SQL)
		if err != nil {
			return Query{}, err
		}
		variants[i].serverBindingSQL, variants[i].ParamIndexes, err = serverPositionalSQL(variants[i].SQL, params)
		if err != nil {
			return Query{}, err
		}
	}
	composition := *input.Composition
	composition.Variants = variants
	query, err := mergeQueryVariants(&composition)
	if err != nil {
		return Query{}, compositionError(input, err)
	}
	return query, nil
}
