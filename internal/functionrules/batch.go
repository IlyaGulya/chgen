package functionrules

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

func fixtureExpression(expression, column string) string {
	expression = strings.ReplaceAll(expression, "(c", "("+column)
	return strings.ReplaceAll(expression, ", c", ", "+column)
}

// A refused batch is not evidence about its individual expressions. Retry
// those separately so one unsupported overload cannot contaminate its peers.
func (s server) batch(ctx context.Context, table string, columns map[string]string, probes []probe) ([]Cell, bool, error) {
	var projections, executions []string
	for i, probe := range probes {
		expression := fixtureExpression(probe.expression, columns[probe.input])
		projections = append(projections, fmt.Sprintf("%s AS r%d", expression, i))
		executions = append(executions, "toTypeName("+expression+")", "ignore("+expression+")")
	}
	analysis, code, err := s.query(ctx, "DESCRIBE TABLE (SELECT "+strings.Join(projections, ",")+" FROM "+table+") FORMAT JSON")
	if err != nil || code != 0 {
		return nil, false, err
	}
	var describe struct {
		Data []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(analysis), &describe); err != nil || len(describe.Data) != len(probes) {
		return nil, false, fmt.Errorf("invalid batch DESCRIBE witness")
	}
	execution, code, err := s.query(ctx, "SELECT "+strings.Join(executions, ",")+" FROM "+table+" FORMAT TabSeparated")
	if err != nil || code != 0 {
		return nil, false, err
	}
	rows := strings.Split(strings.TrimSpace(execution), "\n")
	if len(rows) != 4 {
		return nil, false, fmt.Errorf("invalid batch execution row count")
	}
	cells := make([]Cell, len(probes))
	for i, probe := range probes {
		if describe.Data[i].Name != fmt.Sprintf("r%d", i) || describe.Data[i].Type == "" {
			return nil, false, fmt.Errorf("invalid batch projection witness")
		}
		cells[i] = Cell{ID: probe.id, Input: probe.input, Expression: probe.expression, Values: slices.Clone(probe.values), Analysis: describe.Data[i].Type, ExecutionRows: 4}
	}
	for _, row := range rows {
		fields := strings.Split(row, "\t")
		if len(fields) != len(probes)*2 {
			return nil, false, fmt.Errorf("invalid batch execution field count")
		}
		for i := range cells {
			if fields[2*i] == "" || fields[2*i+1] != "0" || (cells[i].Execution != "" && cells[i].Execution != fields[2*i]) {
				return nil, false, fmt.Errorf("invalid batch execution type witness")
			}
			cells[i].Execution = fields[2*i]
		}
	}
	return cells, true, nil
}
