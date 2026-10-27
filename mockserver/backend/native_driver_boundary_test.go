//go:build cgo

package backend

import "testing"

func TestNativeDriverRejectsClosedHandle(t *testing.T) {
	for _, operation := range []struct {
		name string
		call func(nativeDriver)
	}{
		{"read", func(driver nativeDriver) { driver.read(1, 0, 0, true, false) }},
		{"transaction", func(driver nativeDriver) { driver.check(1, nil, true) }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			driver := newNativeDriver(Serializable)
			driver.close()
			defer func() {
				if recover() == nil {
					t.Fatal("closed native handle was treated as a consistency decision")
				}
			}()
			operation.call(driver)
		})
	}
}
