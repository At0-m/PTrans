package domain

import "testing"

func TestRefundTransitions(t *testing.T) {
	statuses := []RefundStatus{RefundPending, RefundProcessing, RefundSucceeded, RefundFailed, "INVALID"}
	allowed := map[[2]RefundStatus]bool{
		{RefundPending, RefundProcessing}:   true,
		{RefundProcessing, RefundSucceeded}: true,
		{RefundProcessing, RefundFailed}:    true,
		{RefundFailed, RefundProcessing}:    true,
	}
	for _, from := range statuses {
		for _, to := range statuses {
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				err := ValidateRefundTransition(from, to)
				if (err == nil) != allowed[[2]RefundStatus{from, to}] {
					t.Fatalf("transition %s -> %s: %v", from, to, err)
				}
			})
		}
	}
}
