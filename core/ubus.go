package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// ubusCall je jedino mjesto u Core-u koje dohvaća stanje sustava.
// ubus vraća čisti JSON pa nema parsiranja tekstualnih izlaza (D-007).
func ubusCall(ctx context.Context, object, method string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "ubus", "call", object, method).Output()
	if err != nil {
		return fmt.Errorf("ubus call %s %s: %w", object, method, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("ubus %s %s decode: %w", object, method, err)
	}
	return nil
}
