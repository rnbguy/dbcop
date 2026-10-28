package backend

import "testing"

func TestSQLIndexedTextNumericEquality(t *testing.T) {
	f := newSQLTableFixture(t)
	for _, query := range []string{
		"CREATE DATABASE coerciondb", "USE coerciondb",
		"CREATE TABLE entries (id INT PRIMARY KEY, label VARCHAR(16), prefix_label VARCHAR(16), INDEX by_label(label), INDEX by_prefix(prefix_label(1)))",
		"INSERT INTO entries VALUES (1,'01','01'),(2,'1','1'),(3,'1.0','1.0'),(4,'2','2')",
	} {
		f.query(t, query)
	}
	for _, predicate := range []string{"label=1", "label <=> 1", "prefix_label=1", "prefix_label <=> 1", "label<2", "prefix_label<2"} {
		t.Run(predicate, func(t *testing.T) {
			got, err := sqlReplayQuery(f.engine, f.ctx, "SELECT id FROM entries WHERE "+predicate+" ORDER BY id", true)
			if err != nil {
				t.Fatal(err)
			}
			semanticWant(t, got, semanticInts(1, 2, 3))
		})
	}
	for _, predicate := range []string{"label='01'", "prefix_label='01'"} {
		t.Run(predicate, func(t *testing.T) {
			got, err := sqlReplayQuery(f.engine, f.ctx, "SELECT id FROM entries WHERE "+predicate+" ORDER BY id", true)
			if err != nil {
				t.Fatal(err)
			}
			semanticWant(t, got, semanticInts(1))
		})
	}
}
