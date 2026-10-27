package main

import (
	"math"
	"math/big"
	"testing"
)

func tpccTestMoney(t *testing.T, value string) *big.Rat {
	t.Helper()
	amount, ok := new(big.Rat).SetString(value)
	if !ok {
		t.Fatalf("invalid fixture amount %q", value)
	}
	return amount
}

// Two warehouses and districts reuse local IDs to exercise every composite key.
// Each district has two delivered orders and one pending-only customer.
func tpccTestState(t *testing.T) *tpccState {
	t.Helper()
	s := newTPCCState()
	for w := uint64(1); w <= 2; w++ {
		s.warehouses[w] = tpccTestMoney(t, "60.00")
		for d := uint64(1); d <= 2; d++ {
			district := tpccDistrictKey{warehouse: w, district: d}
			s.districts[district] = tpccDistrict{ytd: tpccTestMoney(t, "30.00"), next: 4}
			for id := uint64(1); id <= 3; id++ {
				customer := tpccCustomerKey{district: district, id: id}
				s.customers[customer] = tpccCustomer{balance: tpccTestMoney(t, "-10.00"), payment: tpccTestMoney(t, "10.00")}
				amount := tpccTestMoney(t, "10.00")
				tpccAdd(s.historyWarehouse, w, amount)
				tpccAdd(s.historyDistrict, district, amount)
				tpccAdd(s.historyCustomer, customer, amount)
				order := tpccOrderKey{district: district, id: id}
				s.orders[order] = tpccOrder{customer: id, lines: 2, carrier: id < 3}
				line := &tpccLines{count: 2}
				if id < 3 {
					line.deliveredCount = 2
				} else {
					s.newOrders[order] = struct{}{}
				}
				s.lines[order] = line
			}
		}
	}
	return s
}

func tpccTestAssertions(t *testing.T, s *tpccState, broken ...int) {
	t.Helper()
	result, err := s.check()
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 12 {
		t.Fatalf("got %d assertions, want 12", len(result))
	}
	wantBroken := make(map[int]bool, len(broken))
	for _, id := range broken {
		wantBroken[id] = true
	}
	ids := []string{"A1", "A2", "A3", "A4", "A5", "A6", "A7", "A8", "A9", "A10", "A11", "A12"}
	for i, assertion := range result {
		if assertion.ID != ids[i] {
			t.Errorf("assertion %d has ID %q, want %q", i, assertion.ID, ids[i])
		}
		if assertion.Holds == wantBroken[i+1] {
			t.Errorf("%s Holds = %v, want %v", assertion.ID, assertion.Holds, !wantBroken[i+1])
		}
	}
}

func TestTPCCAssertionsValidAndCorruptStates(t *testing.T) {
	district := tpccDistrictKey{warehouse: 1, district: 1}
	otherDistrict := tpccDistrictKey{warehouse: 1, district: 2}
	first := tpccOrderKey{district: district, id: 1}
	second := tpccOrderKey{district: district, id: 2}
	pending := tpccOrderKey{district: district, id: 3}
	customer := tpccCustomerKey{district: district, id: 1}
	tests := []struct {
		name   string
		change func(*tpccState)
		broken []int
	}{
		{"valid", func(*tpccState) {}, nil},
		{"A1 warehouse district totals", func(s *tpccState) {
			s.warehouses[1].Add(s.warehouses[1], tpccTestMoney(t, "0.01"))
		}, []int{1, 8}},
		{"A2 next order ID", func(s *tpccState) {
			d := s.districts[district]
			d.next++
			s.districts[district] = d
		}, []int{2}},
		{"A3 pending sequence hole", func(s *tpccState) {
			s.newOrders[tpccOrderKey{district: district, id: 5}] = struct{}{}
		}, []int{2, 3, 5, 11}},
		{"A4 district line total", func(s *tpccState) {
			o := s.orders[first]
			o.lines++
			s.orders[first] = o
		}, []int{4, 6}},
		{"A5 pending carrier", func(s *tpccState) {
			o := s.orders[pending]
			o.carrier = true
			s.orders[pending] = o
		}, []int{5, 7}},
		{"A6 per-order counts despite matching district sum", func(s *tpccState) {
			s.lines[first].count++
			s.lines[first].deliveredCount++
			s.lines[second].count--
			s.lines[second].deliveredCount--
		}, []int{6}},
		{"A7 mixed delivered and undelivered lines", func(s *tpccState) {
			s.lines[first].deliveredCount--
		}, []int{7}},
		{"A8 payment history warehouse total", func(s *tpccState) {
			amount := tpccTestMoney(t, "0.01")
			tpccAdd(s.historyWarehouse, uint64(1), amount)
			tpccAdd(s.historyDistrict, district, amount)
			tpccAdd(s.historyCustomer, customer, amount)
		}, []int{8, 9, 10}},
		{"A9 payment district independent of customer home", func(s *tpccState) {
			tpccAdd(s.historyDistrict, district, tpccTestMoney(t, "-10.00"))
			tpccAdd(s.historyDistrict, otherDistrict, tpccTestMoney(t, "10.00"))
		}, []int{9}},
		{"A10 customer balance history", func(s *tpccState) {
			c := s.customers[customer]
			c.balance.Add(c.balance, tpccTestMoney(t, "0.01"))
		}, []int{10, 12}},
		{"A11 historical fixture threshold", func(s *tpccState) {
			delete(s.orders, first)
			delete(s.lines, first)
		}, []int{11}},
		{"A12 exact payment equality without epsilon", func(s *tpccState) {
			c := s.customers[customer]
			c.payment.Add(c.payment, tpccTestMoney(t, "0.0000000001"))
		}, []int{12}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := tpccTestState(t)
			test.change(s)
			tpccTestAssertions(t, s, test.broken...)
		})
	}
}

func TestTPCCDeliveredAmountsAndRemoteHistory(t *testing.T) {
	s := tpccTestState(t)
	district := tpccDistrictKey{warehouse: 1, district: 1}
	customer := tpccCustomerKey{district: district, id: 1}
	order := tpccOrderKey{district: district, id: 1}
	// This customer has two delivered orders; their exact decimal sum is 0.30.
	s.lines[order].delivered.Set(tpccTestMoney(t, "0.10"))
	second := tpccOrderKey{district: district, id: 4}
	s.orders[second] = tpccOrder{customer: 1, lines: 1, carrier: true}
	s.lines[second] = &tpccLines{count: 1, deliveredCount: 1}
	s.lines[second].delivered.Set(tpccTestMoney(t, "0.20"))
	// Move the pending order to ID 5 so both the next-ID and pending-max assertions hold.
	pending := tpccOrderKey{district: district, id: 3}
	moved := tpccOrderKey{district: district, id: 5}
	s.orders[moved], s.lines[moved] = s.orders[pending], s.lines[pending]
	delete(s.orders, pending)
	delete(s.lines, pending)
	delete(s.newOrders, pending)
	s.newOrders[moved] = struct{}{}
	d := s.districts[district]
	d.next = 6
	s.districts[district] = d
	c := s.customers[customer]
	c.balance.Set(tpccTestMoney(t, "-9.70"))
	// A remote payment updates payment warehouse 2 but customer home warehouse 1.
	remote := tpccDistrictKey{warehouse: 2, district: 2}
	payment := tpccTestMoney(t, "1.23")
	tpccAdd(s.historyWarehouse, remote.warehouse, payment)
	tpccAdd(s.historyDistrict, remote, payment)
	tpccAdd(s.historyCustomer, customer, payment)
	s.warehouses[remote.warehouse].Add(s.warehouses[remote.warehouse], payment)
	rd := s.districts[remote]
	rd.ytd.Add(rd.ytd, payment)
	c.balance.Sub(c.balance, payment)
	c.payment.Add(c.payment, payment)
	tpccTestAssertions(t, s)
}

func TestTPCCRejectsMissingAndExtraDomains(t *testing.T) {
	district := tpccDistrictKey{warehouse: 1, district: 1}
	order := tpccOrderKey{district: district, id: 1}
	customer := tpccCustomerKey{district: district, id: 1}
	tests := []struct {
		name   string
		change func(*tpccState)
		broken []int
	}{
		{"zero-valued warehouse without districts or history", func(s *tpccState) {
			s.warehouses[3] = tpccTestMoney(t, "0")
		}, []int{1, 8}},
		{"missing complete order-line group", func(s *tpccState) {
			delete(s.lines, order)
		}, []int{4, 6, 7}},
		{"missing customer history group", func(s *tpccState) {
			delete(s.historyCustomer, customer)
		}, []int{10}},
		{"orphan zero-valued customer history", func(s *tpccState) {
			s.historyCustomer[tpccCustomerKey{district: district, id: 99}] = tpccTestMoney(t, "0")
		}, []int{10}},
		{"orphan delivered order-line group", func(s *tpccState) {
			key := tpccOrderKey{district: district, id: 99}
			s.lines[key] = &tpccLines{count: 1, deliveredCount: 1}
		}, []int{4, 6, 7, 10, 12}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := tpccTestState(t)
			test.change(s)
			tpccTestAssertions(t, s, test.broken...)
		})
	}
}

func TestTPCCAllowsEmptyPendingQueues(t *testing.T) {
	s := tpccTestState(t)
	clear(s.newOrders)
	// cr02 makes pending MAX optional; cr05 does not assert the reverse implication.
	tpccTestAssertions(t, s)
}

func TestTPCCRejectsEmptyRequiredProjections(t *testing.T) {
	for _, table := range []string{"WAREHOUSE", "DISTRICT", "OORDER", "CUSTOMER", "HISTORY", "ORDER_LINE"} {
		t.Run(table, func(t *testing.T) {
			s := tpccTestState(t)
			switch table {
			case "WAREHOUSE":
				clear(s.warehouses)
			case "DISTRICT":
				clear(s.districts)
			case "OORDER":
				clear(s.orders)
			case "CUSTOMER":
				clear(s.customers)
			case "HISTORY":
				clear(s.historyWarehouse)
			case "ORDER_LINE":
				clear(s.lines)
			}
			if assertions, err := s.check(); err == nil || assertions != nil {
				t.Fatalf("got assertions %v, error %v; want incomplete-data error", assertions, err)
			}
		})
	}
}

func TestTPCCDecimalScanner(t *testing.T) {
	for _, test := range []struct {
		input any
		want  string
	}{
		{[]byte("-10.00"), "-10"},
		{"0.10", "1/10"},
		{"1.2e-3", "3/2500"},
		{"12.345678901234567890", "1234567890123456789/100000000000000000"},
		{int64(42), "42"},
		{float64(0.1), "1/10"},
	} {
		var amount tpccDecimal
		if err := amount.Scan(test.input); err != nil {
			t.Errorf("Scan(%v): %v", test.input, err)
			continue
		}
		if amount.value.Cmp(tpccTestMoney(t, test.want)) != 0 {
			t.Errorf("Scan(%v) = %s, want %s", test.input, &amount.value, test.want)
		}
	}
	for _, input := range []any{nil, "", "NULL", "NaN", "Inf", "1/2", "0x10", "1_000", " 1", "1\n", math.Inf(1), math.NaN()} {
		var amount tpccDecimal
		if err := amount.Scan(input); err == nil {
			t.Errorf("Scan(%v) accepted invalid SQL decimal", input)
		}
	}
}
