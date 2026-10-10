package sqlir

// WalkSelect visits every SELECT, including queries nested in expressions,
// relation trees, CTEs and set operations. The callback may annotate a SELECT;
// it must not change the tree topology during traversal.
func WalkSelect(query *Select, visit func(*Select)) {
	visit(query)
	if query.Inner != nil {
		WalkSelect(query.Inner, visit)
	}
	for index := range query.SetOperations {
		WalkSelect(&query.SetOperations[index].Query, visit)
	}
	for index := range query.With {
		cte := &query.With[index]
		if cte.Expr != nil {
			walkExprSelects(*cte.Expr, visit)
		} else {
			WalkSelect(&cte.Query, visit)
		}
	}
	for _, item := range query.Items {
		walkExprSelects(item.Expr, visit)
	}
	for _, expression := range selectExtraExpressions(*query) {
		walkExprSelects(expression, visit)
	}
	for _, expression := range []*Expr{query.Where, query.Having, query.Limit, query.Offset} {
		if expression != nil {
			walkExprSelects(*expression, visit)
		}
	}
	for _, expression := range query.GroupBy {
		walkExprSelects(expression, visit)
	}
	for _, order := range query.OrderBy {
		walkExprSelects(order.Expr, visit)
	}
	for _, definition := range query.Windows {
		walkExprSelects(Expr{Window: &definition.Spec}, visit)
	}
	for index := range query.From {
		walkRelationSelects(&query.From[index], visit)
	}
}

func walkExprSelects(expression Expr, visit func(*Select)) {
	if expression.Query != nil {
		WalkSelect(expression.Query, visit)
	}
	for _, argument := range expression.Args {
		walkExprSelects(argument, visit)
	}
	for _, argument := range expression.Parameters {
		walkExprSelects(argument, visit)
	}
	if window := expression.Window; window != nil {
		for _, partition := range window.PartitionBy {
			walkExprSelects(partition, visit)
		}
		for _, order := range window.OrderBy {
			walkExprSelects(order.Expr, visit)
		}
		if window.Frame != nil {
			walkExprSelects(*window.Frame, visit)
		}
	}
}

func walkRelationSelects(relation *Relation, visit func(*Select)) {
	if relation.Query != nil {
		WalkSelect(relation.Query, visit)
	}
	if relation.Call != nil {
		walkExprSelects(*relation.Call, visit)
	}
	for _, item := range relation.Items {
		walkExprSelects(item.Expr, visit)
	}
	for _, expression := range relation.On {
		walkExprSelects(expression, visit)
	}
	for _, expression := range relation.Using {
		walkExprSelects(expression, visit)
	}
	if relation.Left != nil {
		walkRelationSelects(relation.Left, visit)
	}
	if relation.Right != nil {
		walkRelationSelects(relation.Right, visit)
	}
}
