package rooms

import (
	"fmt"
	"sync"
	"testing"
)

func TestReconcileSignalsCoalesceAndKeepHosts(t *testing.T) {
	var signals reconcileSignals
	var writers sync.WaitGroup
	for index := 0; index < 1000; index++ {
		writers.Add(1)
		go func(index int) { defer writers.Done(); signals.notify(fmt.Sprintf("room-%d.example", index%20)) }(index)
	}
	writers.Wait()
	if len(signals.channel()) != 1 {
		t.Fatal("notifications did not coalesce into one pending signal")
	}
	hosts, all := signals.take()
	if len(hosts) != 20 || all {
		t.Fatal("room probe hints were lost")
	}
	if len(signals.channel()) != 0 {
		t.Fatal("consumed notification remained queued")
	}
	signals.notify("ROOM-1.EXAMPLE.")
	hosts, all = signals.take()
	if len(hosts) != 1 || !hosts["room-1.example"] || all {
		t.Fatal("next notification was not independently delivered")
	}
	for index := 0; index < 300; index++ {
		signals.notify(fmt.Sprintf("room-%d.example", index))
	}
	hosts, all = signals.take()
	if len(hosts) != 256 || !all {
		t.Fatal("notification storage is not bounded")
	}
}
