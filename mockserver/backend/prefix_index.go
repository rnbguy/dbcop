package backend

import (
	"fmt"
	"unicode/utf8"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/encodings"
)

// Prefix postings narrow equality probes only. Range predicates still use the
// retained full cell values, including independently chosen historical versions.
func (t *Table) indexEqualityKey(ctx *sql.Context, index *tableIndex, position int, value interface{}) (string, bool, error) {
	value, err := t.indexValue(ctx, index, position, value)
	if err != nil {
		return "", false, err
	}
	return equalityKey(ctx, t.def.Columns[index.columns[position]].Type, value)
}

func (t *Table) indexValue(ctx *sql.Context, index *tableIndex, position int, value interface{}) (interface{}, error) {
	lengths := index.PrefixLengths()
	if value == nil || position >= len(lengths) || lengths[position] == 0 {
		return value, nil
	}
	typ := t.def.Columns[index.columns[position]].Type.(sql.StringType)
	str, ok := value.(string)
	if bytes, binary := value.([]byte); binary {
		str, ok = encodings.BytesToString(bytes), true
	}
	if !ok {
		converted, _, err := typ.Convert(ctx, value)
		if err != nil {
			return nil, err
		}
		converted, err = sql.UnwrapAny(ctx, converted)
		if err != nil {
			return nil, err
		}
		switch converted := converted.(type) {
		case string:
			str = converted
		case []byte:
			str = encodings.BytesToString(converted)
		default:
			return nil, fmt.Errorf("unexpected index string value %T", converted)
		}
	}
	length := int(lengths[position])
	if typ.CharacterSet() == sql.CharacterSet_binary {
		if len(str) > length {
			str = str[:length]
		}
		return str, nil
	}
	offset := 0
	for count := 0; count < length && offset < len(str); count++ {
		r, size := utf8.DecodeRuneInString(str[offset:])
		if size <= 0 || (r == utf8.RuneError && size == 1) {
			return nil, fmt.Errorf("malformed string in index prefix for %s", t.def.Columns[index.columns[position]].Name)
		}
		offset += size
	}
	return str[:offset], nil
}

func (index *tableIndex) CanSupport(ctx *sql.Context, ranges ...sql.Range) bool {
	if len(index.PrefixLengths()) == 0 {
		return index.Index.CanSupport(ctx, ranges...)
	}
	// Do not promise ordered prefix ranges: full-value residual filtering is
	// required at an open boundary sharing the same stored prefix.
	for _, candidate := range ranges {
		bounds, ok := candidate.(sql.MySQLRange)
		if !ok || len(bounds) > len(index.columns) {
			return false
		}
		for position, bound := range bounds {
			if index.PrefixLengths()[position] == 0 {
				continue
			}
			switch bound.Type() {
			case sql.RangeType_All, sql.RangeType_Empty, sql.RangeType_EqualNull:
			case sql.RangeType_ClosedClosed:
				cmp, err := bound.Typ.Compare(ctx, sql.GetMySQLRangeCutKey(bound.LowerBound), sql.GetMySQLRangeCutKey(bound.UpperBound))
				if err != nil || cmp != 0 {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}
