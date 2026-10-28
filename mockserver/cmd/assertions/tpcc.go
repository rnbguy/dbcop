package main

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"math/big"
	"strconv"
	"strings"
)

type tpccDistrictKey struct {
	warehouse uint64
	district  uint64
}

type tpccOrderKey struct {
	district tpccDistrictKey
	id       uint64
}

type tpccCustomerKey struct {
	district tpccDistrictKey
	id       uint64
}

type tpccDistrict struct {
	ytd  *big.Rat
	next uint64
}

type tpccOrder struct {
	customer uint64
	lines    uint64
	carrier  bool
}

type tpccCustomer struct {
	balance *big.Rat
	payment *big.Rat
}

type tpccLines struct {
	count          uint64
	deliveredCount uint64
	delivered      big.Rat
}

type tpccState struct {
	warehouses       map[uint64]*big.Rat
	districts        map[tpccDistrictKey]tpccDistrict
	orders           map[tpccOrderKey]tpccOrder
	newOrders        map[tpccOrderKey]struct{}
	customers        map[tpccCustomerKey]tpccCustomer
	historyWarehouse map[uint64]*big.Rat
	historyDistrict  map[tpccDistrictKey]*big.Rat
	historyCustomer  map[tpccCustomerKey]*big.Rat
	lines            map[tpccOrderKey]*tpccLines
}

func newTPCCState() *tpccState {
	return &tpccState{
		warehouses:       make(map[uint64]*big.Rat),
		districts:        make(map[tpccDistrictKey]tpccDistrict),
		orders:           make(map[tpccOrderKey]tpccOrder),
		newOrders:        make(map[tpccOrderKey]struct{}),
		customers:        make(map[tpccCustomerKey]tpccCustomer),
		historyWarehouse: make(map[uint64]*big.Rat),
		historyDistrict:  make(map[tpccDistrictKey]*big.Rat),
		historyCustomer:  make(map[tpccCustomerKey]*big.Rat),
		lines:            make(map[tpccOrderKey]*tpccLines),
	}
}

// MySQL DECIMAL and FLOAT text are parsed exactly; FLOAT storage drift is not rounded away.
type tpccDecimal struct {
	value big.Rat
}

func (d *tpccDecimal) Scan(src any) error {
	var text string
	switch value := src.(type) {
	case []byte:
		text = string(value)
	case string:
		text = value
	case int64:
		text = strconv.FormatInt(value, 10)
	case float32:
		text = strconv.FormatFloat(float64(value), 'g', -1, 32)
	case float64:
		text = strconv.FormatFloat(value, 'g', -1, 64)
	default:
		return fmt.Errorf("invalid TPCC decimal value %T", src)
	}
	if strings.ContainsAny(text, "/xXpP_ \t\r\n") {
		return fmt.Errorf("invalid TPCC decimal %q", text)
	}
	if _, ok := d.value.SetString(text); !ok {
		return fmt.Errorf("invalid TPCC decimal %q", text)
	}
	return nil
}

func tpccAdd[K comparable](totals map[K]*big.Rat, key K, amount *big.Rat) {
	if total, ok := totals[key]; ok {
		total.Add(total, amount)
	} else {
		totals[key] = new(big.Rat).Set(amount)
	}
}

func tpccReadRows(ctx context.Context, tx *sql.Tx, query string, visit func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("%s: %w", query, err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := visit(rows); err != nil {
			return fmt.Errorf("%s: %w", query, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%s: %w", query, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("%s: %w", query, err)
	}
	return nil
}

func checkTPCC(ctx context.Context, tx *sql.Tx) ([]Assertion, error) {
	s := newTPCCState()
	var amount tpccDecimal
	if err := tpccReadRows(ctx, tx, "SELECT W_ID, W_YTD FROM WAREHOUSE", func(rows *sql.Rows) error {
		var id uint64
		if err := rows.Scan(&id, &amount); err != nil {
			return err
		}
		if _, exists := s.warehouses[id]; exists {
			return fmt.Errorf("duplicate warehouse key %d", id)
		}
		s.warehouses[id] = new(big.Rat).Set(&amount.value)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := tpccReadRows(ctx, tx, "SELECT D_W_ID, D_ID, D_YTD, D_NEXT_O_ID FROM DISTRICT", func(rows *sql.Rows) error {
		var key tpccDistrictKey
		var next uint64
		if err := rows.Scan(&key.warehouse, &key.district, &amount, &next); err != nil {
			return err
		}
		if _, exists := s.districts[key]; exists {
			return fmt.Errorf("duplicate district key %+v", key)
		}
		s.districts[key] = tpccDistrict{ytd: new(big.Rat).Set(&amount.value), next: next}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := tpccReadRows(ctx, tx, "SELECT O_W_ID, O_D_ID, O_ID, O_C_ID, O_OL_CNT, O_CARRIER_ID FROM OORDER", func(rows *sql.Rows) error {
		var key tpccOrderKey
		var order tpccOrder
		var carrier sql.NullInt64
		if err := rows.Scan(&key.district.warehouse, &key.district.district, &key.id, &order.customer, &order.lines, &carrier); err != nil {
			return err
		}
		if _, exists := s.orders[key]; exists {
			return fmt.Errorf("duplicate order key %+v", key)
		}
		order.carrier = carrier.Valid
		s.orders[key] = order
		return nil
	}); err != nil {
		return nil, err
	}
	if err := tpccReadRows(ctx, tx, "SELECT NO_W_ID, NO_D_ID, NO_O_ID FROM NEW_ORDER", func(rows *sql.Rows) error {
		var key tpccOrderKey
		if err := rows.Scan(&key.district.warehouse, &key.district.district, &key.id); err != nil {
			return err
		}
		if _, exists := s.newOrders[key]; exists {
			return fmt.Errorf("duplicate new-order key %+v", key)
		}
		s.newOrders[key] = struct{}{}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := tpccReadRows(ctx, tx, "SELECT C_W_ID, C_D_ID, C_ID, C_BALANCE, C_YTD_PAYMENT FROM CUSTOMER", func(rows *sql.Rows) error {
		var key tpccCustomerKey
		var payment tpccDecimal
		if err := rows.Scan(&key.district.warehouse, &key.district.district, &key.id, &amount, &payment); err != nil {
			return err
		}
		if _, exists := s.customers[key]; exists {
			return fmt.Errorf("duplicate customer key %+v", key)
		}
		s.customers[key] = tpccCustomer{balance: new(big.Rat).Set(&amount.value), payment: new(big.Rat).Set(&payment.value)}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := tpccReadRows(ctx, tx, "SELECT H_W_ID, H_D_ID, H_C_W_ID, H_C_D_ID, H_C_ID, H_AMOUNT FROM HISTORY", func(rows *sql.Rows) error {
		var district tpccDistrictKey
		var customer tpccCustomerKey
		if err := rows.Scan(&district.warehouse, &district.district, &customer.district.warehouse, &customer.district.district, &customer.id, &amount); err != nil {
			return err
		}
		tpccAdd(s.historyWarehouse, district.warehouse, &amount.value)
		tpccAdd(s.historyDistrict, district, &amount.value)
		tpccAdd(s.historyCustomer, customer, &amount.value)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := tpccReadRows(ctx, tx, "SELECT OL_W_ID, OL_D_ID, OL_O_ID, OL_AMOUNT, OL_DELIVERY_D FROM ORDER_LINE", func(rows *sql.Rows) error {
		var key tpccOrderKey
		var delivery sql.NullString
		if err := rows.Scan(&key.district.warehouse, &key.district.district, &key.id, &amount, &delivery); err != nil {
			return err
		}
		line := s.lines[key]
		if line == nil {
			line = &tpccLines{}
			s.lines[key] = line
		}
		line.count++
		if delivery.Valid {
			line.deliveredCount++
			line.delivered.Add(&line.delivered, &amount.value)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return s.check()
}

func tpccSameDomain[K comparable, A, B any](a map[K]A, b map[K]B) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if _, exists := b[key]; !exists {
			return false
		}
	}
	return true
}

func tpccSameMoney[K comparable](a, b map[K]*big.Rat) bool {
	if !tpccSameDomain(a, b) {
		return false
	}
	for key, amount := range a {
		if amount.Cmp(b[key]) != 0 {
			return false
		}
	}
	return true
}

func (s *tpccState) check() ([]Assertion, error) {
	for _, table := range []struct {
		name string
		rows int
	}{
		{"WAREHOUSE", len(s.warehouses)},
		{"DISTRICT", len(s.districts)},
		{"OORDER", len(s.orders)},
		{"CUSTOMER", len(s.customers)},
		{"HISTORY", len(s.historyWarehouse)},
		{"ORDER_LINE", len(s.lines)},
	} {
		if table.rows == 0 {
			return nil, fmt.Errorf("incomplete TPCC data: %s is empty", table.name)
		}
	}

	result := make([]Assertion, 12)
	for i := range result {
		result[i] = Assertion{ID: fmt.Sprintf("A%d", i+1), Holds: true}
	}
	districtYTD := make(map[tpccDistrictKey]*big.Rat, len(s.districts))
	warehouseDistrictYTD := make(map[uint64]*big.Rat, len(s.warehouses))
	for key, district := range s.districts {
		districtYTD[key] = district.ytd
		tpccAdd(warehouseDistrictYTD, key.warehouse, district.ytd)
	}
	// A1: warehouse YTD equals the sum of district YTD, with the same warehouse domain.
	result[0].Holds = tpccSameMoney(s.warehouses, warehouseDistrictYTD)

	orderMax := make(map[tpccDistrictKey]uint64)
	orderCount := make(map[tpccDistrictKey]uint64)
	orderLineCount := make(map[tpccDistrictKey]uint64)
	for key, order := range s.orders {
		if id, exists := orderMax[key.district]; !exists || key.id > id {
			orderMax[key.district] = key.id
		}
		orderCount[key.district]++
		orderLineCount[key.district] += order.lines
		if _, exists := s.customers[tpccCustomerKey{district: key.district, id: order.customer}]; !exists {
			result[9].Holds = false
			result[11].Holds = false
		}
	}
	type pendingRange struct{ min, max, count uint64 }
	pending := make(map[tpccDistrictKey]pendingRange)
	for key := range s.newOrders {
		span, exists := pending[key.district]
		if !exists {
			span.min, span.max = key.id, key.id
		} else {
			span.min = min(span.min, key.id)
			span.max = max(span.max, key.id)
		}
		span.count++
		pending[key.district] = span
		if _, exists := s.districts[key.district]; !exists {
			result[1].Holds = false
			result[2].Holds = false
			result[10].Holds = false
		}
		// A5 is the artifact's one-way pending-order/non-NULL-carrier exclusion.
		if order, exists := s.orders[key]; !exists || order.carrier {
			result[4].Holds = false
		}
	}

	// A2 permits an empty pending queue, as does the artifact's explicit optional check.
	if !tpccSameDomain(s.districts, orderMax) {
		result[1].Holds = false
	}
	for key, district := range s.districts {
		id, exists := orderMax[key]
		if !exists || district.next == 0 || district.next-1 != id {
			result[1].Holds = false
		}
		if span, exists := pending[key]; exists && span.max != id {
			result[1].Holds = false
		}
		// A11 follows cr11's executable >= 2 threshold, not its contradictory SQL comment.
		pendingCount := pending[key].count
		if count := orderCount[key]; count < pendingCount || count-pendingCount < 2 {
			result[10].Holds = false
		}
	}
	if !tpccSameDomain(s.districts, orderCount) {
		result[10].Holds = false
	}
	// A3: no holes in each nonempty pending-order interval.
	for _, span := range pending {
		if span.max-span.min != span.count-1 {
			result[2].Holds = false
		}
	}

	actualLineCount := make(map[tpccDistrictKey]uint64)
	delivered := make(map[tpccCustomerKey]*big.Rat)
	if !tpccSameDomain(s.orders, s.lines) {
		result[5].Holds = false
		result[6].Holds = false
	}
	for key, line := range s.lines {
		actualLineCount[key.district] += line.count
		order, exists := s.orders[key]
		// A6: each order's declared line count equals its actual line count.
		if !exists || order.lines != line.count {
			result[5].Holds = false
		}
		// A7 checks every line, rather than retaining only the last line scanned by cr07.
		if !exists || (order.carrier && line.deliveredCount != line.count) || (!order.carrier && line.deliveredCount != 0) {
			result[6].Holds = false
		}
		if line.deliveredCount == 0 {
			continue
		}
		customer := tpccCustomerKey{district: key.district, id: order.customer}
		if _, known := s.customers[customer]; !exists || !known {
			result[9].Holds = false
			result[11].Holds = false
			continue
		}
		tpccAdd(delivered, customer, &line.delivered)
	}
	// A4: district-level declared and actual line totals have identical key domains.
	result[3].Holds = tpccSameDomain(s.districts, orderLineCount) && maps.Equal(orderLineCount, actualLineCount)
	// A8 and A9 group payment HISTORY by payment warehouse/district, not customer home.
	result[7].Holds = tpccSameMoney(s.warehouses, s.historyWarehouse)
	result[8].Holds = tpccSameMoney(districtYTD, s.historyDistrict)

	if !tpccSameDomain(s.customers, s.historyCustomer) {
		result[9].Holds = false
	}
	var zero, want big.Rat
	for key, customer := range s.customers {
		amount := delivered[key]
		if amount == nil {
			amount = &zero
		}
		// A10: balance = delivered order-line amounts - all customer payment history.
		if history, exists := s.historyCustomer[key]; !exists || want.Sub(amount, history).Cmp(customer.balance) != 0 {
			result[9].Holds = false
		}
		// A12: balance + YTD payment = delivered amounts, including customers with none.
		if want.Add(customer.balance, customer.payment).Cmp(amount) != 0 {
			result[11].Holds = false
		}
	}
	return result, nil
}
