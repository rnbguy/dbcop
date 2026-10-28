package main

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

func checkApplications(ctx context.Context, tx *sql.Tx, benchmark string) ([]Assertion, error) {
	switch benchmark {
	case "smallbank":
		accounts, err := appReadCounts(ctx, tx, "SELECT custid FROM accounts")
		if err != nil {
			return nil, err
		}
		savings, err := appReadBalances(ctx, tx, "SELECT custid, bal FROM savings")
		if err != nil {
			return nil, err
		}
		checking, err := appReadBalances(ctx, tx, "SELECT custid, bal FROM checking")
		if err != nil {
			return nil, err
		}
		assertion, err := appSmallBankAssertion(accounts, savings, checking)
		if err != nil {
			return nil, err
		}
		return []Assertion{assertion}, nil
	case "voter":
		contestants, err := appReadCounts(ctx, tx, "SELECT contestant_number FROM CONTESTANTS")
		if err != nil {
			return nil, err
		}
		if len(contestants) == 0 {
			return nil, fmt.Errorf("voter: empty contestant fixture")
		}
		votes, err := appReadCounts(ctx, tx, "SELECT phone_number FROM VOTES")
		if err != nil {
			return nil, err
		}
		return []Assertion{{ID: "A14", Holds: appVoteCapHolds(votes)}}, nil
	case "wikipedia":
		users, err := appReadUserEdits(ctx, tx)
		if err != nil {
			return nil, err
		}
		recent, err := appReadCounts(ctx, tx, "SELECT rc_user FROM recentchanges")
		if err != nil {
			return nil, err
		}
		logs, err := appReadCounts(ctx, tx, "SELECT log_user FROM logging")
		if err != nil {
			return nil, err
		}
		return appWikipediaAssertions(users, recent, logs)
	default:
		return nil, fmt.Errorf("unsupported application benchmark %q", benchmark)
	}
}

func appReadCounts(ctx context.Context, tx *sql.Tx, query string) (map[uint64]uint64, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", query, err)
	}
	defer rows.Close()
	counts := make(map[uint64]uint64)
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("%s: %w", query, err)
		}
		counts[id]++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", query, err)
	}
	return counts, nil
}

func appReadBalances(ctx context.Context, tx *sql.Tx, query string) (map[uint64]*big.Rat, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", query, err)
	}
	defer rows.Close()
	balances := make(map[uint64]*big.Rat)
	for rows.Next() {
		var id uint64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("%s: %w", query, err)
		}
		if _, exists := balances[id]; exists {
			return nil, fmt.Errorf("%s: duplicate customer %d", query, id)
		}
		balance, err := appParseBalance(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: customer %d: %w", query, id, err)
		}
		balances[id] = balance
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", query, err)
	}
	return balances, nil
}

func appParseBalance(raw string) (*big.Rat, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || strings.ContainsAny(raw, "/_xXpP") {
		return nil, fmt.Errorf("invalid balance %q", raw)
	}
	balance, ok := new(big.Rat).SetString(raw)
	if !ok {
		return nil, fmt.Errorf("invalid balance %q", raw)
	}
	return balance, nil
}

func appSmallBankAssertion(accounts map[uint64]uint64, savings, checking map[uint64]*big.Rat) (Assertion, error) {
	result := Assertion{ID: "A13"}
	if len(accounts) == 0 {
		return result, fmt.Errorf("smallbank: empty account fixture")
	}
	for id, count := range accounts {
		if count != 1 {
			return result, fmt.Errorf("smallbank: duplicate account %d", id)
		}
	}
	if len(savings) != len(accounts) || len(checking) != len(accounts) {
		return result, nil
	}
	total := new(big.Rat)
	for id := range accounts {
		saving, savingsOK := savings[id]
		check, checkingOK := checking[id]
		if !savingsOK || !checkingOK {
			return result, nil
		}
		total.Add(total, saving)
		total.Add(total, check)
	}
	// The artifact starts both balances at 2500 and runs only conserving transactions.
	want := new(big.Rat).Mul(big.NewRat(5000, 1), new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(len(accounts)))))
	result.Holds = total.Cmp(want) == 0
	return result, nil
}

// BenchBase VoterConstants.MAX_VOTES is 1000; the paper artifact instead checks 2.
const appVoterMaxVotes uint64 = 1000

func appVoteCapHolds(votes map[uint64]uint64) bool {
	for _, count := range votes {
		if count > appVoterMaxVotes {
			return false
		}
	}
	return true
}

func appReadUserEdits(ctx context.Context, tx *sql.Tx) (map[uint64]uint64, error) {
	const query = "SELECT user_id, user_editcount FROM useracct"
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", query, err)
	}
	defer rows.Close()
	users := make(map[uint64]uint64)
	for rows.Next() {
		var id, edits uint64
		if err := rows.Scan(&id, &edits); err != nil {
			return nil, fmt.Errorf("%s: %w", query, err)
		}
		if _, exists := users[id]; exists {
			return nil, fmt.Errorf("%s: duplicate user %d", query, id)
		}
		users[id] = edits
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", query, err)
	}
	return users, nil
}

func appCountsEqual(left, right map[uint64]uint64) bool {
	for id, count := range left {
		if count != right[id] {
			return false
		}
	}
	for id, count := range right {
		if count != left[id] {
			return false
		}
	}
	return true
}

func appWikipediaAssertions(users, recent, logs map[uint64]uint64) ([]Assertion, error) {
	if len(users) == 0 {
		return nil, fmt.Errorf("wikipedia: empty user fixture")
	}
	return []Assertion{
		{ID: "A15", Holds: appCountsEqual(users, recent)},
		{ID: "A16", Holds: appCountsEqual(users, logs)},
		{ID: "A17", Holds: appCountsEqual(recent, logs)},
	}, nil
}
