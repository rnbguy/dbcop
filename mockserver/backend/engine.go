package backend

import (
	"reflect"
	"slices"
	"strings"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/analyzer"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/plan"
	"github.com/dolthub/go-mysql-server/sql/transform"
)

// NewEngine adds the FilteredTable dispatch missing from go-mysql-server v0.20.
// Run after binding, index selection, and projection; retain engine filters too.
func NewEngine(provider *Provider) *sqle.Engine {
	a := analyzer.NewBuilder(provider).AddPreValidationRule(-1, pushReadFilters).Build()
	return sqle.New(a, nil)
}

func pushReadFilters(ctx *sql.Context, _ *analyzer.Analyzer, node sql.Node, _ *plan.Scope, _ analyzer.RuleSelector, _ *sql.QueryFlags) (sql.Node, transform.TreeIdentity, error) {
	return transform.Node(node, func(node sql.Node) (sql.Node, transform.TreeIdentity, error) {
		filter, ok := node.(*plan.Filter)
		if !ok || !filter.Resolved() {
			return node, transform.SameTree, nil
		}
		child, changed, err := attachReadFilters(ctx, filter.Child, expression.SplitConjunction(filter.Expression))
		if err != nil || !changed {
			return node, transform.SameTree, err
		}
		updated, err := filter.WithChildren(child)
		return updated, transform.NewTree, err
	})
}

func attachReadFilters(ctx *sql.Context, node sql.Node, filters []sql.Expression) (sql.Node, bool, error) {
	switch node := node.(type) {
	case *plan.ResolvedTable:
		table, changed := filteredTable(ctx, node.Table, filters)
		if !changed {
			return node, false, nil
		}
		updated, err := node.WithTable(table)
		return updated, true, err
	case *plan.IndexedTableAccess:
		table, changed := filteredTable(ctx, node.Table, filters)
		if !changed {
			return node, false, nil
		}
		updated := *node
		updated.Table = table.(sql.IndexedTable)
		return &updated, true, nil
	case *plan.TableAlias:
		underlying, ok := node.Child.(sql.Nameable)
		if !ok {
			return node, false, nil
		}
		normalized := make([]sql.Expression, len(filters))
		for i, filter := range filters {
			expr, _, err := transform.Expr(filter, func(expr sql.Expression) (sql.Expression, transform.TreeIdentity, error) {
				if field, ok := expr.(*expression.GetField); ok && strings.EqualFold(field.Table(), node.Name()) {
					return field.WithTable(underlying.Name()), transform.NewTree, nil
				}
				return expr, transform.SameTree, nil
			})
			if err != nil {
				return node, false, err
			}
			normalized[i] = expr
		}
		child, changed, err := attachReadFilters(ctx, node.Child, normalized)
		if err != nil || !changed {
			return node, false, err
		}
		updated, err := node.WithChildren(child)
		return updated, true, err
	default:
		return node, false, nil
	}
}

func filteredTable(ctx *sql.Context, table sql.Table, filters []sql.Expression) (sql.Table, bool) {
	filtered, ok := table.(sql.FilteredTable)
	if !ok {
		return table, false
	}
	combined := append([]sql.Expression(nil), filtered.Filters()...)
	for _, filter := range filtered.HandledFilters(filters) {
		if !slices.ContainsFunc(combined, func(existing sql.Expression) bool {
			return reflect.DeepEqual(existing, filter)
		}) {
			combined = append(combined, filter)
		}
	}
	if len(combined) == len(filtered.Filters()) {
		return table, false
	}
	return filtered.WithFilters(ctx, combined), true
}
