package service

import (
	"fmt"
	"sync/atomic"
	"time"
)

var globalIDSeq atomic.Uint64

func nextID(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UTC().UnixNano(), globalIDSeq.Add(1))
}

func generateEventID() string {
	return nextID("evt")
}

func generatePaymentID() string {
	return nextID("pay")
}

func generateSubscriptionID() string {
	return nextID("sub")
}