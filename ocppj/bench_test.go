// SPDX-License-Identifier: MIT

package ocppj

import (
	"testing"

	"github.com/otfabric/go-ocpp"
)

// BenchmarkDecode measures framing and limit checks on one CALL frame.
func BenchmarkDecode(b *testing.B) {
	frame := []byte(`[2,"19223201","BootNotification",{"chargePointVendor":"v","chargePointModel":"m"}]`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Decode(frame, ocpp.V16, Limits{}); err != nil {
			b.Fatal(err)
		}
	}
}
